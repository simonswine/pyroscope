package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractMinio(t *testing.T) {
	for _, tt := range []struct {
		name    string
		headers []tar.Header
		wantErr bool
	}{
		{"binary", []tar.Header{{Name: "minio", Typeflag: tar.TypeReg, Size: 4}}, false},
		{"nested", []tar.Header{{Name: "release/minio", Typeflag: tar.TypeReg, Size: 4}}, false},
		{"traversal", []tar.Header{{Name: "../minio", Typeflag: tar.TypeReg, Size: 4}}, true},
		{"symlink", []tar.Header{{Name: "minio", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}, true},
		{"duplicate", []tar.Header{{Name: "minio", Typeflag: tar.TypeReg, Size: 4}, {Name: "minio", Typeflag: tar.TypeReg, Size: 4}}, true},
		{"missing", []tar.Header{{Name: "README", Typeflag: tar.TypeReg, Size: 4}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "release.tar.gz")
			file, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(file)
			tw := tar.NewWriter(gz)
			for i := range tt.headers {
				if err := tw.WriteHeader(&tt.headers[i]); err != nil {
					t.Fatal(err)
				}
				if tt.headers[i].Size > 0 {
					if _, err := tw.Write([]byte("test")); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, err := range []error{tw.Close(), gz.Close(), file.Close()} {
				if err != nil {
					t.Fatal(err)
				}
			}
			dest := filepath.Join(dir, "installed")
			err = extractMinio(archive, dest)
			if (err != nil) != tt.wantErr {
				t.Fatalf("extract error=%v, wantErr=%v", err, tt.wantErr)
			}
			if !tt.wantErr {
				data, err := os.ReadFile(dest)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != "test" {
					t.Fatal("extracted content differs")
				}
			}
		})
	}
}

// Optional local validation of a checksum-verified release without executing it.
func TestMinioReleaseArchitecture(t *testing.T) {
	archive := os.Getenv("MINIO_TEST_ARCHIVE")
	if archive == "" {
		t.Skip("set MINIO_TEST_ARCHIVE to a downloaded release")
	}
	dest := filepath.Join(t.TempDir(), "minio")
	if err := extractMinio(archive, dest); err != nil {
		t.Fatal(err)
	}
	if err := linuxAMD64(dest); err != nil {
		t.Fatal(err)
	}
}
