package hygiene

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func expectFinding(t *testing.T, dir string) []Finding {
	t.Helper()
	findings, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("scanner did not detect a planted marker")
	}
	return findings
}

// TestDetectsAssistantNameInComment and the neighbours below prove the
// high-confidence content families are detected in code comments.
func TestDetectsAssistantNameInComment(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "review.go", "package x\n\n// Reviewed by "+cat("chat", "gpt")+" before merge.\n")
	expectFinding(t, dir)
}

func TestDetectsConversationalNote(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "note.go", "package x\n\n// From the "+cat("previous ", "ch", "at")+": keep this.\n")
	expectFinding(t, dir)
}

func TestDetectsSavedStateReference(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "note.md", "Status captured in "+cat("check", "point")+" 2026-11-05.\n")
	expectFinding(t, dir)
}

func TestDetectsDecisionProvenanceReference(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "note.md", "Per the "+cat("ow", "ner")+", the "+cat("pro", "ject coordinator")+" approved it.\n")
	expectFinding(t, dir)
}

func TestDetectsPersonalPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "path.go", "package x\n\nconst p = \""+cat("/ho", "me/alice/src")+"\"\n")
	expectFinding(t, dir)
}

func TestDetectsPrivateMarkerInLogging(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "log.go", "package x\n\nfunc f() { println(\""+cat("ow", "ner", "_verify")+"\") }\n")
	expectFinding(t, dir)
}

func TestDetectsMarkerInRootFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ROOTNOTES.txt", "leak: "+cat("mu", "se")+"\n")
	expectFinding(t, dir)
}

func TestDetectsMarkerInFileName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, cat("chi", "se")+"_notes.md", "clean content\n")
	expectFinding(t, dir)
}

func TestDetectsMarkerInDirectoryName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, cat("mu", "se")+"/clean.txt", "clean content\n")
	expectFinding(t, dir)
}

func TestDetectsAccidentalPublicAgentFile(t *testing.T) {
	dir := t.TempDir()
	name := cat("AG", "ENTS.md")
	writeFile(t, dir, name, "# private instructions\n")
	findings, err := ScanFile(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("an accidental public agent file must be a boundary finding")
	}
}

func TestDetectsMarkerInPNGTextChunk(t *testing.T) {
	dir := t.TempDir()
	png := pngWithText("Comment", cat("check", "point")+"-leak")
	writeFile(t, dir, "image.png", string(png))
	expectFinding(t, dir)
}

func TestDetectsMarkerInOpaqueBinary(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "blob.bin", string([]byte{0, 1, 2, 3})+cat("ow", "ner", "_verify")+string([]byte{0}))
	expectFinding(t, dir)
}

func pngWithText(keyword, text string) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(data)))
		b.Write(l[:])
		b.WriteString(typ)
		b.Write(data)
		b.Write([]byte{0, 0, 0, 0})
	}
	chunk("IHDR", make([]byte, 13))
	chunk("tEXt", append([]byte(keyword+"\x00"), []byte(text)...))
	chunk("IEND", nil)
	return b.Bytes()
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestScanTrackedRejectsPrivateInstructionFile(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	name := cat("AG", "ENTS.md")
	writeFile(t, dir, name, "private\n")
	gitRun(t, dir, "add", "-f", name)
	findings, err := ScanTracked(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("a tracked private instruction file must be reported")
	}
}

func TestScanTrackedAllowsVendoredAgentFile(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	writeFile(t, dir, "patcher/vendor/mod/"+cat("AG", "ENTS.md"), "upstream\n")
	gitRun(t, dir, "add", "-f", "patcher/vendor/mod/"+cat("AG", "ENTS.md"))
	findings, err := ScanTracked(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("vendored upstream files are legitimate: %v", findings)
	}
}

func TestPrivateBoundaryRequiresIgnore(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	name := cat("AG", "ENTS.md")
	writeFile(t, dir, name, "private\n")

	findings, err := CheckPrivateBoundary(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("a present but non-ignored private instruction file must fail")
	}

	ex := filepath.Join(dir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(ex), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ex, []byte("/"+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err = CheckPrivateBoundary(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("ignored private instruction file must pass: %v", findings)
	}
}

func TestPrivateBoundaryRejectsNonIgnoredPrivateDir(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	privateDir := cat(".open", "code")
	writeFile(t, dir, privateDir+"/settings.json", "{}\n")
	findings, err := CheckPrivateBoundary(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("a non-ignored private config directory must fail")
	}
}

func TestPrivateBoundaryRejectsGitignoreEntry(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	name := cat("AG", "ENTS.md")
	writeFile(t, dir, name, "private\n")
	writeFile(t, dir, ".gitignore", "/"+name+"\n")
	findings, err := CheckPrivateBoundary(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("agent instructions must not be moved into .gitignore")
	}
}

func TestCheckReleaseTreeRejectsPrivateFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, cat("AG", "ENTS.md"), "private\n")
	writeFile(t, dir, ".local/keys/prod.keys", "key\n")
	writeFile(t, dir, "docs/README.md", "public documentation\n")

	findings, err := CheckReleaseTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) < 2 {
		t.Fatalf("private release files must be reported: %v", findings)
	}
}

func TestCheckReleaseTreeAcceptsCleanTree(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "13s-patcher", "binary\n")
	writeFile(t, dir, "version.txt", "0.4.3\n")
	writeFile(t, dir, "docs/README.md", "public documentation\n")
	findings, err := CheckReleaseTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("clean release tree must pass: %v", findings)
	}
}

func TestAcceptsLegitimateTechnicalVocabulary(t *testing.T) {
	text := "The software model has a worker pool, a user prompt, a token parser,\n" +
		"a cursor position, a system status and a reasoning step.\n"
	if got := scanBytes("doc.md", []byte(text), markers()); len(got) > 0 {
		t.Fatalf("legitimate technical vocabulary must not be flagged: %v", got)
	}
}

func TestAcceptsAuthoredGameDialogue(t *testing.T) {
	dir := t.TempDir()
	personWord := cat("ow", "ner")
	stateWord := cat("check", "point")
	record := `{"bank_key":"fms/scripttext.fms","record_index":1,"text":"The ` + personWord +
		` of the glasses waits at the ` + stateWord + ` with the agents."}` + "\n"
	writeFile(t, dir, "changes.jsonl", record)
	writeFile(t, dir, "manifest.json", "{}\n")
	findings, err := ScanDataset(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("authored text must be exempt: %v", findings)
	}

	bad := `{"bank_key":"fms/` + personWord + `.fms","record_index":1,"text":"hi"}` + "\n"
	writeFile(t, dir, "changes.jsonl", bad)
	findings, err = ScanDataset(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("a marker in a structural field must still be reported")
	}
}

func TestAcceptsVendorUpstreamAgentContent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "patcher/vendor/mod/"+cat("AG", "ENTS.md"),
		"Retrieve comments from GitHub "+cat("Co", "pilot")+" or Cursor BugBot.\nUpstream notes are fine.\n")
	findings, err := ScanVendor(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("upstream vendor content is legitimate: %v", findings)
	}
}

func TestAcceptsDocumentedPrivateStateReference(t *testing.T) {
	text := "In user mode the tool keeps state in the documented private data directory;\n" +
		"the project checkout documents .local/keys and .local/13s-patcher.\n"
	if got := scanBytes("doc.md", []byte(text), markers()); len(got) > 0 {
		t.Fatalf("documented user-state references must not be flagged: %v", got)
	}
}

func TestFixSafeFormatsGoAndNotProse(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt is not installed")
	}
	dir := t.TempDir()
	goFile := writeFile(t, dir, "x.go", "package x\n\n// keep  this   comment\nfunc  f( ) { }\n")
	mdFile := writeFile(t, dir, "a.md", "a line with trailing spaces   \n")
	jsonFile := writeFile(t, dir, "a.json", "{\"a\": 1}   \n")

	touched, err := FixSafe(dir, []string{"x.go", "a.md", "a.json"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(touched, ","), "a.md") {
		t.Fatal("prose must never be auto-rewritten")
	}
	raw, _ := os.ReadFile(mdFile)
	if !strings.Contains(string(raw), "trailing spaces   \n") {
		t.Fatal("Markdown was modified")
	}
	raw, _ = os.ReadFile(goFile)
	if !strings.Contains(string(raw), "// keep  this   comment") {
		t.Fatal("comment text must be preserved by gofmt")
	}
	raw, _ = os.ReadFile(jsonFile)
	if strings.Contains(string(raw), "   \n") {
		t.Fatal("machine-readable trailing whitespace was not removed")
	}
}
