package extraction

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Tool is one discovered decode helper.
type Tool struct {
	Name    string // executable name as found on PATH
	Path    string // absolute path, when found
	Role    string // what it is used for
	Install string // short install hint shown when the tool is missing
}

// KnownTools lists the helpers this tool can drive.
var KnownTools = []Tool{
	{Name: "hactool", Role: "decode the program NCA and reconstruct the update RomFS", Install: "build or install hactool and put it on PATH"},
	{Name: "nstool", Role: "unpack the containers and report the content keys", Install: "build or install nstool and put it on PATH"},
	{Name: "hactoolnet", Role: "alternative NSP unpacker (not used by the built-in chain)", Install: "install hactoolnet (dotnet tool) and put the wrapper on PATH"},
}

// DiscoverTools reports which helpers are on PATH, in preference order.
func DiscoverTools() []Tool {
	var out []Tool
	for _, t := range KnownTools {
		if p, err := exec.LookPath(t.Name); err == nil {
			t.Path = p
			out = append(out, t)
		}
	}
	return out
}

// ToolHelp renders what is available and how to install what is missing.
func ToolHelp() string {
	var b strings.Builder
	found := DiscoverTools()
	if len(found) == 0 {
		b.WriteString("no container helper was found on PATH\n")
	} else {
		b.WriteString("container helpers found:\n")
		for _, t := range found {
			fmt.Fprintf(&b, "  %-12s %s\n", t.Name, t.Path)
		}
	}
	b.WriteString("if you want the tool to unpack the containers itself, install one of:\n")
	for _, t := range KnownTools {
		fmt.Fprintf(&b, "  %-12s %s\n", t.Name, t.Install)
	}
	return b.String()
}

// ToolExtractor drives a discovered helper to unpack the containers. It builds
// the command itself, so no command template has to be configured by hand.
//
// The argument shapes below are the documented unpack options of each helper.
// The unpack step itself cannot be exercised offline, so its first run against
// a real container is the point at which it is proven.
type ToolExtractor struct {
	Tool Tool
}

// Describe implements Extractor.
func (t *ToolExtractor) Describe() string {
	return "nstool + hactool (container unpack)"
}

// Available implements Extractor. This title's update carries a patch RomFS, so
// the built-in decoder needs both helpers: nstool unpacks the containers and
// reports the content keys, and hactool reconstructs the update RomFS over the
// decrypted base NCA.
func (t *ToolExtractor) Available() error {
	var missing []string
	for _, name := range []string{"nstool", "hactool"} {
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the built-in container decoder needs both nstool and hactool on PATH (missing: %s)", strings.Join(missing, ", "))
	}
	return nil
}

// Extract implements Extractor. It drives nstool to unpack each container into
// its NCAs and hactool to decode the program NCA. The 1.10 update carries a
// patch RomFS, so the base program NCA is needed as the base for the update;
// the resulting merged RomFS supplies both the update overlay and the base
// members the build consumes.
//
// The two helpers read the user's key file themselves. This package only passes
// its path; no key material is read, copied or logged here.
func (t *ToolExtractor) Extract(ctx context.Context, req ExtractRequest) error {
	if err := t.Available(); err != nil {
		return err
	}
	want := map[Artifact]bool{}
	for _, a := range req.Artifacts {
		want[a] = true
	}
	if req.BaseContainer == "" || req.UpdateContainer == "" {
		return errors.New("both the base and the update container are required: the update carries a patch RomFS whose base is the base program NCA")
	}
	keysFile := ""
	if req.KeysDir != "" {
		keysFile = filepath.Join(req.KeysDir, "prod.keys")
	}

	baseNSP := filepath.Join(req.DestDir, "base-nsp")
	updateNSP := filepath.Join(req.DestDir, "update-nsp")
	if err := os.MkdirAll(baseNSP, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(updateNSP, 0o755); err != nil {
		return err
	}
	if err := runTool(ctx, "nstool", "-k", keysFile, "-x", "/", baseNSP, req.BaseContainer); err != nil {
		return fmt.Errorf("unpacking the base container: %w", err)
	}
	if err := runTool(ctx, "nstool", "-k", keysFile, "-x", "/", updateNSP, req.UpdateContainer); err != nil {
		return fmt.Errorf("unpacking the update container: %w", err)
	}

	baseNCA, baseTik, err := findProgramNCA(ctx, keysFile, baseNSP)
	if err != nil {
		return fmt.Errorf("base container: %w", err)
	}
	updateNCA, updateTik, err := findProgramNCA(ctx, keysFile, updateNSP)
	if err != nil {
		return fmt.Errorf("update container: %w", err)
	}
	baseKey, err := contentKey(ctx, keysFile, baseTik, baseNCA)
	if err != nil {
		return fmt.Errorf("base container: %w", err)
	}
	updateKey, err := contentKey(ctx, keysFile, updateTik, updateNCA)
	if err != nil {
		return fmt.Errorf("update container: %w", err)
	}

	// A plaintext base NCA is what the update's patch RomFS is read against.
	// It is a scratch intermediate: it is removed before the merged tree is
	// staged and never enters the cache.
	plainBase := filepath.Join(req.DestDir, "base-plain.nca")
	if err := runTool(ctx, "hactool", "-t", "nca", "-k", keysFile,
		"--contentkey="+baseKey, "--plaintext="+plainBase, baseNCA); err != nil {
		return fmt.Errorf("decoding the base program NCA: %w", err)
	}
	// The base NCAs are no longer needed once the plaintext copy exists.
	_ = os.RemoveAll(baseNSP)
	merged := filepath.Join(req.DestDir, "romfs")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		return err
	}
	if err := runTool(ctx, "hactool", "-t", "nca", "-k", keysFile,
		"--contentkey="+updateKey, "--basenca="+plainBase, "--romfsdir="+merged, updateNCA); err != nil {
		return fmt.Errorf("decoding the update RomFS over the base: %w", err)
	}
	_ = os.Remove(plainBase)
	_ = os.RemoveAll(updateNSP)

	if want[ArtifactOverlay] {
		src := findNamedFile(merged, "ROBO_PATCH_US.CPK")
		if src == "" {
			return errors.New("the decoded game tree holds no update overlay archive")
		}
		if err := stageOverlay(filepath.Dir(src), filepath.Join(req.DestDir, OverlayDir)); err != nil {
			return err
		}
	}
	if want[ArtifactBaseBanks] || want[ArtifactBaseMembers] {
		found, err := stageCPKArtifacts(merged,
			filepath.Join(req.DestDir, BanksSubdir),
			filepath.Join(req.DestDir, MembersSubdir),
			req.BaseMembers, DefaultBankNaming)
		if err != nil {
			return err
		}
		if !found && (want[ArtifactBaseBanks] || want[ArtifactBaseMembers]) {
			return errors.New("the decoded game tree holds no base archive")
		}
	}
	return nil
}

// programNCA finds the program NCA and its ticket in an unpacked container.
func findProgramNCA(ctx context.Context, keysFile, dir string) (nca, tik string, err error) {
	var ncas []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if strings.HasSuffix(name, ".tik") && tik == "" {
			tik = path
		}
		if strings.HasSuffix(name, ".nca") && !strings.HasSuffix(name, ".cnmt.nca") {
			ncas = append(ncas, path)
		}
		return nil
	})
	if tik == "" {
		return "", "", errors.New("the unpacked container holds no ticket")
	}
	for _, cand := range ncas {
		out, rerr := runToolOutput(ctx, "nstool", "-k", keysFile, cand)
		if rerr != nil {
			continue
		}
		if lineHasContentType(out, "Program") {
			return cand, tik, nil
		}
	}
	return "", "", errors.New("the unpacked container holds no program NCA")
}

// contentKey reads the title's content key from nstool's key report. The key is
// passed straight to hactool; it is never recorded in a report or log.
func contentKey(ctx context.Context, keysFile, tik, nca string) (string, error) {
	out, err := runToolOutput(ctx, "nstool", "-k", keysFile, "--tik", tik, "--showkeys", nca)
	if err != nil {
		return "", err
	}
	inSection := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "[NCA Content Key]") {
			inSection = true
			continue
		}
		if inSection {
			if v := hexAfter(line, "AES-CTR Key:"); v != "" {
				return v, nil
			}
		}
	}
	return "", errors.New("nstool reported no content key")
}

// lineHasContentType reports whether a tool report names the given NCA content
// type. The report aligns its columns with spaces, so the value is matched as a
// word after the label.
func lineHasContentType(out, want string) bool {
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, "Content Type:")
		if i < 0 {
			continue
		}
		for _, f := range strings.Fields(line[i+len("Content Type:"):]) {
			if f == want {
				return true
			}
		}
	}
	return false
}

// hexAfter returns the first hex token after label on a line.
func hexAfter(line, label string) string {
	i := strings.Index(line, label)
	if i < 0 {
		return ""
	}
	for _, f := range strings.Fields(line[i+len(label):]) {
		if len(f) == 32 {
			ok := true
			for _, r := range f {
				if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
					ok = false
					break
				}
			}
			if ok {
				return f
			}
		}
	}
	return ""
}

// runTool runs a helper, discarding normal output and returning a short tail on
// failure.
func runTool(ctx context.Context, name string, args ...string) error {
	_, err := runToolOutput(ctx, name, args...)
	return err
}

func runToolOutput(ctx context.Context, name string, args ...string) (string, error) {
	if _, lerr := exec.LookPath(name); lerr != nil {
		return "", fmt.Errorf("%s was not found on PATH", name)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return string(out), ctx.Err()
		}
		return string(out), fmt.Errorf("%s failed: %w (%s)", name, err, tail(string(out), 3))
	}
	return string(out), nil
}

// findNamedFile returns the first regular file with the given base name.
func findNamedFile(root, name string) string {
	var found string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" || d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), name) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// stageOverlay copies the accepted overlay archive out of an unpacked tree.
func stageOverlay(unpackedDir, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	src := ""
	filepath.WalkDir(unpackedDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || src != "" || d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), "ROBO_PATCH_US.CPK") {
			src = path
			return filepath.SkipAll
		}
		return nil
	})
	if src == "" {
		return fmt.Errorf("the unpacked update tree holds no patch archive")
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dstDir, filepath.Base(src)), raw, 0o644)
}

// stageBanks copies the base text banks out of an unpacked tree.
func stageBanks(unpackedDir, dstDir string, naming BankNaming) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	n := 0
	err := filepath.WalkDir(unpackedDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(strings.ToLower(name), strings.ToLower(naming.Suffix)) {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		dst := filepath.Join(dstDir, strings.TrimSuffix(name, naming.Suffix)+naming.Suffix)
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("the unpacked base tree holds no text banks")
	}
	return nil
}

// KeysNotice is the exact message shown when extraction is needed and the key
// files are missing. It names the expected file names and the directory a
// checkout keeps them in.
func KeysNotice(keysDir string) string {
	var b strings.Builder
	b.WriteString("Switch keys are required to extract the game files.\n\n")
	b.WriteString("Place your own key files in:\n\n")
	b.WriteString(".local/keys/\n\n")
	b.WriteString("Expected:\n  prod.keys\n  title.keys (if required)\n\n")
	b.WriteString("Press Enter to check again.")
	if keysDir != "" && filepath.Base(filepath.Dir(keysDir)) != "local" {
		fmt.Fprintf(&b, "\n\nConfigured keys directory: %s", keysDir)
	}
	return b.String()
}
