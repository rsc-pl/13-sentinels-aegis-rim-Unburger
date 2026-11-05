package extraction

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"13s-patcher/internal/synth"
)

// TestStageCPKArtifacts proves the base archive is read from an unpacked game
// tree and its logical text banks and requested members are staged under the
// documented flattened names.
func TestStageCPKArtifacts(t *testing.T) {
	dir := t.TempDir()
	unpacked := filepath.Join(dir, "unpacked", "1")
	if err := os.MkdirAll(unpacked, 0o755); err != nil {
		t.Fatal(err)
	}
	script := synth.Bank([]string{"Alpha", "Beta"})
	sysmsg := synth.Bank([]string{"Gamma", strings.Repeat("longer line ", 40)})
	plate := []byte("synthetic plate script")
	title := []byte("synthetic title atlas")
	archive, err := synth.Archive([]synth.Member{
		{Dir: "Fms", Name: "ScriptText.fms", Logical: script, ID: 1},
		{Dir: "Fms", Name: "SysMsg.fms", Logical: sysmsg, Compress: true, ID: 2},
		{Dir: "GUI", Name: "change_kurabe.mbs", Logical: plate, ID: 3},
		{Dir: "GUI", Name: "title.ftx", Logical: title, ID: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unpacked, "ROBO_US.CPK"), archive, 0o644); err != nil {
		t.Fatal(err)
	}
	banks := filepath.Join(dir, "banks")
	members := filepath.Join(dir, "members")
	found, err := stageCPKArtifacts(filepath.Join(dir, "unpacked"), banks, members,
		[]string{"GUI/change_kurabe.mbs", "GUI/title.ftx"}, DefaultBankNaming)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("the base archive was not found")
	}
	for name, want := range map[string][]byte{
		"Fms__ScriptText.fms": script,
		"Fms__SysMsg.fms":     sysmsg,
	} {
		got, rerr := os.ReadFile(filepath.Join(banks, name))
		if rerr != nil {
			t.Fatal(rerr)
		}
		if string(got) != string(want) {
			t.Fatalf("%s logical bytes differ", name)
		}
	}
	for name, want := range map[string][]byte{
		"GUI__change_kurabe.mbs": plate,
		"GUI__title.ftx":         title,
	} {
		got, rerr := os.ReadFile(filepath.Join(members, name))
		if rerr != nil {
			t.Fatal(rerr)
		}
		if string(got) != string(want) {
			t.Fatalf("%s logical bytes differ", name)
		}
	}

	if _, err := stageCPKArtifacts(filepath.Join(dir, "unpacked"), banks, members,
		[]string{"GUI/absent.mbs"}, DefaultBankNaming); err == nil {
		t.Fatal("a required member the archive lacks must fail closed")
	}

	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	found, err = stageCPKArtifacts(filepath.Join(dir, "empty"), banks, members, nil, DefaultBankNaming)
	if err != nil || found {
		t.Fatalf("no archive must report not found (found=%v err=%v)", found, err)
	}
}
