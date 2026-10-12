package env

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Files under the tree the server's container reads, bind-mounted from root on this machine (Env.DataDir).

// Mkdir makes a directory under root that the server's own user can write in. The umask narrows what MkdirAll gives, so on Linux the server could not
// delete what the test laid out; chmod is not filtered. Docker Desktop hides this, so it only bites in CI.
func Mkdir(t *testing.T, root, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	for p := dir; strings.HasPrefix(p, root) && p != root; p = filepath.Dir(p) {
		if err := os.Chmod(p, 0o777); err != nil { //nolint:gosec // same
			t.Fatal(err)
		}
	}
}

// WriteFile writes a file the server's own user can write over or remove (see Mkdir).
func WriteFile(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o666); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil { //nolint:gosec // same
		t.Fatal(err)
	}
}

// CopyTree copies a folder under the tree at root, and everything under it, to a new place.
func CopyTree(t *testing.T, root, src, dst string) {
	t.Helper()

	if err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		to := filepath.Join(dst, strings.TrimPrefix(path, src))
		if d.IsDir() {
			Mkdir(t, root, to)

			return nil
		}
		raw, rerr := os.ReadFile(path) //nolint:gosec // a fixture under the test data dir
		if rerr != nil {
			return rerr
		}
		WriteFile(t, to, raw)

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TreeOf reads everything under root, folders ending in "/", so a test can hold the disk to what it was; skip leaves those paths out.
func TreeOf(t *testing.T, root string, skip ...string) map[string][]byte {
	t.Helper()

	out := map[string][]byte{}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case slices.Contains(skip, path) && d.IsDir():
			return filepath.SkipDir
		case slices.Contains(skip, path):
		case d.IsDir():
			out[path+"/"] = nil
		default:
			raw, rerr := os.ReadFile(path) //nolint:gosec // a fixture under the test data dir
			if rerr != nil {
				return rerr
			}
			out[path] = raw
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return out
}

// SameTree fails for every path that changed, went or appeared between two reads of a tree (TreeOf), named without root.
func SameTree(t *testing.T, root string, before, after map[string][]byte) {
	t.Helper()

	for _, problem := range treeChanges(root, before, after) {
		t.Error(problem)
	}
}

// treeChanges names every path that differs between two reads of a tree, in order.
func treeChanges(root string, before, after map[string][]byte) []string {
	var out []string
	for path, raw := range before {
		if now, ok := after[path]; !ok || !bytes.Equal(now, raw) {
			out = append(out, strings.TrimPrefix(path, root)+" changed or went")
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			out = append(out, strings.TrimPrefix(path, root)+" appeared")
		}
	}
	slices.Sort(out)

	return out
}

// FilesUnder reads every file under the folders, to hold them to later (StillOnDisk); folders with nothing under them fail the test.
func FilesUnder(t *testing.T, dirs ...string) map[string][]byte {
	t.Helper()

	out := map[string][]byte{}
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				raw, rerr := os.ReadFile(path) //nolint:gosec // a fixture under the test data dir
				if rerr != nil {
					t.Fatal(rerr)
				}
				out[path] = raw
			}

			return nil
		})
	}
	if len(out) == 0 {
		t.Fatalf("nothing on disk under %v", dirs)
	}

	return out
}

// StillOnDisk checks every file read by FilesUnder is where it was, as it was, after what the test did (after), naming a file without root.
func StillOnDisk(t *testing.T, root string, files map[string][]byte, after string) {
	t.Helper()

	for _, problem := range filesChanged(root, files, after) {
		t.Error(problem)
	}
}

// filesChanged names every file that is no longer on disk as it was read, in order.
func filesChanged(root string, files map[string][]byte, after string) []string {
	var out []string
	for path, raw := range files {
		if now, err := os.ReadFile(path); err != nil || !bytes.Equal(now, raw) { //nolint:gosec // a fixture under the test data dir
			out = append(out, strings.TrimPrefix(path, root)+" went or changed with "+after)
		}
	}
	slices.Sort(out)

	return out
}
