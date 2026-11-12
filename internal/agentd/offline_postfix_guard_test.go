package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

const postfixTestHoldPath = "/var/lib/urmail-backup/upgrade.hold"

type fakePostfixInstance struct {
	observation offlineWriterObservation
	cat         []byte
}

type fakePostfixGuardOps struct {
	units           []string
	instances       map[string]fakePostfixInstance
	templateCat     []byte
	files           map[string]systemdSource
	listErr         error
	observeErr      map[string]error
	catErr          map[string]error
	fileErr         map[string]error
	showCalls       []string
	listCalls       int
	mutateFile      string
	mutateAfterRead int
	mutatedFile     systemdSource
	fileReadCounts  map[string]int
}

func (f *fakePostfixGuardOps) reader() postfixGuardOps {
	return postfixGuardOps{
		list: func(context.Context) ([]string, error) {
			f.listCalls++
			if f.listErr != nil {
				return nil, f.listErr
			}
			return append([]string(nil), f.units...), nil
		},
		observe: func(_ context.Context, unit string) (offlineWriterObservation, error) {
			f.showCalls = append(f.showCalls, unit)
			if unit == "postfix@.service" {
				return offlineWriterObservation{}, errors.New("systemd show rejects template unit")
			}
			if err := f.observeErr[unit]; err != nil {
				return offlineWriterObservation{}, err
			}
			instance, ok := f.instances[unit]
			if !ok {
				return offlineWriterObservation{}, errors.New("unknown fake instance")
			}
			return instance.observation, nil
		},
		cat: func(_ context.Context, unit string) ([]byte, error) {
			if err := f.catErr[unit]; err != nil {
				return nil, err
			}
			if unit == "postfix@.service" {
				return append([]byte(nil), f.templateCat...), nil
			}
			instance, ok := f.instances[unit]
			if !ok {
				return nil, errors.New("unknown fake instance")
			}
			return append([]byte(nil), instance.cat...), nil
		},
		readFile: func(path string) (systemdSource, error) {
			f.fileReadCounts[path]++
			if path == f.mutateFile && f.fileReadCounts[path] > f.mutateAfterRead {
				return f.mutatedFile, nil
			}
			if err := f.fileErr[path]; err != nil {
				return systemdSource{}, err
			}
			source, ok := f.files[path]
			if !ok {
				return systemdSource{}, os.ErrNotExist
			}
			return source, nil
		},
	}
}

func fakeSystemdSource(content string) systemdSource {
	b := []byte(content)
	return systemdSource{Content: b, Fingerprint: hashBytesLocal(b)}
}

func newFakePostfixGuardOps(units ...string) *fakePostfixGuardOps {
	fragment := "/usr/lib/systemd/system/postfix@.service"
	templateGuard := filepath.Join(maintenance.DefaultDropInDir, "postfix@.service.d", maintenance.DropInFileName)
	fragmentContent := "[Unit]\nDescription=Postfix template\n[Service]\nExecStartPre=/usr/lib/postfix/configure-instance.sh %i\n"
	guardContent := "[Unit]\nConditionPathExists=!" + postfixTestHoldPath + "\n[Service]\nExecStartPre=/usr/bin/test ! -f " + postfixTestHoldPath + "\n"
	f := &fakePostfixGuardOps{
		units:          append([]string(nil), units...),
		instances:      map[string]fakePostfixInstance{},
		files:          map[string]systemdSource{fragment: fakeSystemdSource(fragmentContent), templateGuard: fakeSystemdSource(guardContent)},
		observeErr:     map[string]error{},
		catErr:         map[string]error{},
		fileErr:        map[string]error{},
		fileReadCounts: map[string]int{},
	}
	f.templateCat = []byte("# " + fragment + "\n" + fragmentContent + "\n# " + templateGuard + "\n" + guardContent)
	for _, unit := range units {
		instanceGuard := filepath.Join(maintenance.DefaultDropInDir, unit+".d", maintenance.DropInFileName)
		paths := []string{templateGuard}
		cat := "# " + fragment + "\n" + fragmentContent + "\n# " + templateGuard + "\n" + guardContent
		if unit == "postfix@-.service" {
			paths = []string{instanceGuard}
			cat = "# " + fragment + "\n" + fragmentContent + "\n# " + instanceGuard + "\n" + guardContent
			f.files[instanceGuard] = fakeSystemdSource(guardContent)
		}
		pre := "{ path=/usr/lib/postfix/configure-instance.sh ; argv[]=/usr/lib/postfix/configure-instance.sh - ; }\n{ path=/usr/bin/test ; argv[]=/usr/bin/test ! -f " + postfixTestHoldPath + " ; }"
		f.instances[unit] = fakePostfixInstance{
			observation: offlineWriterObservation{Unit: unit, Load: "loaded", Active: "inactive", MainPID: "0", ControlPID: "0", ControlGroup: "", FragmentPath: fragment, DropInPaths: strings.Join(paths, " "), ExecStartPre: pre, NeedDaemonReload: "no"},
			cat:         []byte(cat),
		}
	}
	return f
}

func TestPostfixTemplateGuardUsesCatAndValidatesInheritance(t *testing.T) {
	fake := newFakePostfixGuardOps("postfix@-.service", "postfix@extra.service")
	if _, err := verifyPostfixTemplateAndInstancesWithOps(context.Background(), postfixTestHoldPath, fake.reader()); err != nil {
		t.Fatalf("valid template/instance guards rejected: %v", err)
	}
	for _, unit := range fake.showCalls {
		if unit == "postfix@.service" {
			t.Fatal("systemctl show was incorrectly used on the template")
		}
	}
	if fake.listCalls != 2 {
		t.Fatalf("instance list sampled %d times, want two", fake.listCalls)
	}
}

func TestPostfixTemplateGuardRejectsMissingOrClearedGuard(t *testing.T) {
	t.Run("template guard missing", func(t *testing.T) {
		fake := newFakePostfixGuardOps("postfix@-.service")
		fake.templateCat = []byte(strings.ReplaceAll(string(fake.templateCat), "ConditionPathExists=!"+postfixTestHoldPath+"\n", ""))
		if _, err := verifyPostfixTemplateAndInstancesWithOps(context.Background(), postfixTestHoldPath, fake.reader()); err == nil {
			t.Fatal("missing template guard accepted")
		}
	})
	t.Run("instance override missing", func(t *testing.T) {
		fake := newFakePostfixGuardOps("postfix@-.service")
		unit := "postfix@-.service"
		instanceGuard := filepath.Join(maintenance.DefaultDropInDir, unit+".d", maintenance.DropInFileName)
		missing := "[Unit]\nDescription=unprotected instance\n[Service]\n"
		fake.files[instanceGuard] = fakeSystemdSource(missing)
		instance := fake.instances[unit]
		instance.cat = []byte("# /usr/lib/systemd/system/postfix@.service\n[Service]\nExecStartPre=/usr/lib/postfix/configure-instance.sh %i\n# " + instanceGuard + "\n" + missing)
		fake.instances[unit] = instance
		if _, err := verifyPostfixTemplateAndInstancesWithOps(context.Background(), postfixTestHoldPath, fake.reader()); err == nil {
			t.Fatal("unguarded instance override accepted")
		}
	})
	t.Run("later drop-in clears guard", func(t *testing.T) {
		fake := newFakePostfixGuardOps("postfix@-.service")
		unit := "postfix@-.service"
		clearPath := filepath.Join(maintenance.DefaultDropInDir, unit+".d", "zz-clear.conf")
		clearCfg := "[Unit]\nConditionPathExists=\n[Service]\nExecStartPre=\n"
		fake.files[clearPath] = fakeSystemdSource(clearCfg)
		instance := fake.instances[unit]
		instance.observation.DropInPaths += " " + clearPath
		instance.cat = append(instance.cat, []byte("\n# "+clearPath+"\n"+clearCfg)...)
		fake.instances[unit] = instance
		if _, err := verifyPostfixTemplateAndInstancesWithOps(context.Background(), postfixTestHoldPath, fake.reader()); err == nil {
			t.Fatal("later drop-in that clears the hold guard accepted")
		}
	})
}

func TestPostfixTemplateGuardChecksEveryLoadedInstance(t *testing.T) {
	fake := newFakePostfixGuardOps("postfix@-.service", "postfix@extra.service")
	instance := fake.instances["postfix@extra.service"]
	instance.observation.Active = "active"
	fake.instances["postfix@extra.service"] = instance
	if _, err := verifyPostfixTemplateAndInstancesWithOps(context.Background(), postfixTestHoldPath, fake.reader()); err == nil || !strings.Contains(err.Error(), "not proven inactive") {
		t.Fatalf("active extra Postfix instance accepted: %v", err)
	}
}

func TestPostfixTemplateGuardFailsClosedOnQueriesAndFileChanges(t *testing.T) {
	t.Run("list query failure", func(t *testing.T) {
		fake := newFakePostfixGuardOps("postfix@-.service")
		fake.listErr = errors.New("injected list failure")
		if _, err := verifyPostfixTemplateAndInstancesWithOps(context.Background(), postfixTestHoldPath, fake.reader()); err == nil {
			t.Fatal("list failure accepted")
		}
	})
	t.Run("template cat failure", func(t *testing.T) {
		fake := newFakePostfixGuardOps("postfix@-.service")
		fake.catErr["postfix@.service"] = errors.New("systemctl show unsupported for template")
		if _, err := verifyPostfixTemplateAndInstancesWithOps(context.Background(), postfixTestHoldPath, fake.reader()); err == nil {
			t.Fatal("template query failure accepted")
		}
	})
	t.Run("instance show failure", func(t *testing.T) {
		fake := newFakePostfixGuardOps("postfix@-.service")
		fake.observeErr["postfix@-.service"] = errors.New("injected instance show failure")
		if _, err := verifyPostfixTemplateAndInstancesWithOps(context.Background(), postfixTestHoldPath, fake.reader()); err == nil {
			t.Fatal("instance show failure accepted")
		}
	})
	t.Run("instance cat failure", func(t *testing.T) {
		fake := newFakePostfixGuardOps("postfix@-.service")
		fake.catErr["postfix@-.service"] = errors.New("injected instance cat failure")
		if _, err := verifyPostfixTemplateAndInstancesWithOps(context.Background(), postfixTestHoldPath, fake.reader()); err == nil {
			t.Fatal("instance cat failure accepted")
		}
	})
	t.Run("unsafe or unreadable source", func(t *testing.T) {
		fake := newFakePostfixGuardOps("postfix@-.service")
		fragment := "/usr/lib/systemd/system/postfix@.service"
		fake.fileErr[fragment] = errors.New("injected unsafe fragment metadata")
		if _, err := verifyPostfixTemplateAndInstancesWithOps(context.Background(), postfixTestHoldPath, fake.reader()); err == nil {
			t.Fatal("unsafe source accepted")
		}
	})
	t.Run("template guard changes during proof", func(t *testing.T) {
		fake := newFakePostfixGuardOps("postfix@-.service")
		guard := filepath.Join(maintenance.DefaultDropInDir, "postfix@.service.d", maintenance.DropInFileName)
		fake.mutateFile = guard
		fake.mutateAfterRead = 1
		fake.mutatedFile = fakeSystemdSource("[Unit]\nConditionPathExists=!/changed\n[Service]\nExecStartPre=/usr/bin/test ! -f /changed\n")
		if _, err := verifyPostfixTemplateAndInstancesWithOps(context.Background(), postfixTestHoldPath, fake.reader()); err == nil {
			t.Fatal("template guard change during proof accepted")
		}
	})
}
