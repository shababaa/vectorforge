package byodb

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const crashExitCode = 86

func TestCommitCrashAtomicity(t *testing.T) {
	if stage := os.Getenv("BYODB_TEST_CRASH_STAGE"); stage != "" {
		path := os.Getenv("BYODB_TEST_CRASH_PATH")
		kv, err := OpenKVWithOptions(path, KVOptions{commitFault: func(current commitStage) error {
			if string(current) == stage {
				os.Exit(crashExitCode)
			}
			return nil
		}})
		if err != nil {
			panic(err)
		}
		var tx KVTX
		if err := kv.Begin(&tx); err != nil {
			panic(err)
		}
		tx.Set([]byte("new-commit"), []byte(stage))
		if err := kv.Commit(&tx); err != nil {
			panic(err)
		}
		panic("crash failpoint was not reached")
	}

	tests := []struct {
		stage   commitStage
		newMust bool
	}{
		{stage: commitDataSynced},
		// A metadata write without fsync may or may not survive a real power
		// loss. Recovery must accept exactly the old or new complete root.
		{stage: commitMetaWritten},
		{stage: commitMetaSynced, newMust: true},
	}
	for _, test := range tests {
		t.Run(string(test.stage), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "crash.db")
			kv, err := OpenKV(path)
			if err != nil {
				t.Fatal(err)
			}
			var seed KVTX
			_ = kv.Begin(&seed)
			seed.Set([]byte("stable"), []byte("before"))
			if err := kv.Commit(&seed); err != nil {
				t.Fatal(err)
			}
			if err := kv.Close(); err != nil {
				t.Fatal(err)
			}

			command := exec.Command(os.Args[0], "-test.run=^TestCommitCrashAtomicity$")
			command.Env = append(os.Environ(), "BYODB_TEST_CRASH_STAGE="+string(test.stage), "BYODB_TEST_CRASH_PATH="+path)
			err = command.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != crashExitCode {
				t.Fatalf("child error=%v, want exit %d", err, crashExitCode)
			}

			kv, err = OpenKV(path)
			if err != nil {
				t.Fatalf("recover after %s: %v", test.stage, err)
			}
			defer kv.Close()
			var read KVTX
			_ = kv.Begin(&read)
			stable, ok := read.Get([]byte("stable"))
			if !ok || string(stable) != "before" {
				t.Fatalf("stable commit was lost: (%q,%t)", stable, ok)
			}
			value, newOK := read.Get([]byte("new-commit"))
			if test.stage == commitDataSynced && newOK {
				t.Fatal("data pages became visible before metadata publication")
			}
			if test.newMust && (!newOK || string(value) != string(test.stage)) {
				t.Fatalf("fsynced metadata did not publish commit: (%q,%t)", value, newOK)
			}
			if test.stage == commitMetaWritten && newOK && string(value) != string(test.stage) {
				t.Fatalf("recovery exposed a torn new value: %q", value)
			}
			if err := read.snapshot.validate(); err != nil {
				t.Fatal(err)
			}
			kv.Abort(&read)
		})
	}
}

func TestLongRecoveryChurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "churn.db")
	want := map[string]string{}
	for round := 0; round < 30; round++ {
		kv, err := OpenKV(path)
		if err != nil {
			t.Fatalf("round %d open: %v", round, err)
		}
		var tx KVTX
		if err := kv.Begin(&tx); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 250; index++ {
			key := fmt.Sprintf("market:SYNTH:1d:%04d", index)
			if (index+round)%7 == 0 {
				tx.Del(&DeleteReq{Key: []byte(key)})
				delete(want, key)
			} else {
				value := fmt.Sprintf("round-%02d-value-%04d", round, index)
				tx.Set([]byte(key), []byte(value))
				want[key] = value
			}
		}
		if err := kv.Commit(&tx); err != nil {
			t.Fatalf("round %d commit: %v", round, err)
		}
		if err := kv.Close(); err != nil {
			t.Fatal(err)
		}
	}
	kv, err := OpenKV(path)
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	var read KVTX
	_ = kv.Begin(&read)
	for key, expected := range want {
		value, ok := read.Get([]byte(key))
		if !ok || string(value) != expected {
			t.Fatalf("%s=(%q,%t), want %q", key, value, ok, expected)
		}
	}
	if err := read.snapshot.validate(); err != nil {
		t.Fatal(err)
	}
	kv.Abort(&read)
}
