/* Copyright 2017 The Bazel Authors. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// This file contains integration tests for all of Gazelle. It's meant to test
// common usage patterns and check for errors that are difficult to test in
// unit tests.

package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/tools/gazelle/config"
)

type fileSpec struct {
	path, content string
}

func createFiles(files []fileSpec) (string, error) {
	dir, err := ioutil.TempDir(os.Getenv("TEST_TEMPDIR"), "integration_test")
	if err != nil {
		return "", err
	}

	for _, f := range files {
		path := filepath.Join(dir, filepath.FromSlash(f.path))
		if strings.HasSuffix(f.path, "/") {
			if err := os.MkdirAll(path, 0700); err != nil {
				os.RemoveAll(dir)
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		if err := ioutil.WriteFile(path, []byte(f.content), 0600); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

func checkFiles(t *testing.T, dir string, files []fileSpec) {
	for _, f := range files {
		path := filepath.Join(dir, f.path)
		if strings.HasSuffix(f.path, "/") {
			if st, err := os.Stat(path); err != nil {
				t.Errorf("could not stat %s: %v", f.path, err)
			} else if !st.IsDir() {
				t.Errorf("not a directory: %s", f.path)
			}
		} else {
			gotBytes, err := ioutil.ReadFile(filepath.Join(dir, f.path))
			if err != nil {
				t.Errorf("could not read %s: %v", f.path, err)
				continue
			}
			got := string(gotBytes)
			if got != f.content {
				t.Errorf("%s: got %s ; want %s", f.path, got, f.content)
			}
		}
	}
}

func runGazelle(wd string, args []string) error {
	oldWd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(wd); err != nil {
		return err
	}
	defer os.Chdir(oldWd)

	c, cmd, emit, err := newConfiguration(args)
	if err != nil {
		return err
	}

	run(c, cmd, emit)
	return nil
}

func TestNoRepoRootOrWorkspace(t *testing.T) {
	dir, err := createFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "-repo_root not specified"
	if err := runGazelle(dir, nil); err == nil {
		t.Fatalf("got success; want %q", want)
	} else if !strings.Contains(err.Error(), want) {
		t.Fatalf("got %q; want %q", err, want)
	}
}

func TestNoGoPrefixArgOrRule(t *testing.T) {
	dir, err := createFiles([]fileSpec{
		{path: "WORKSPACE", content: ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "-go_prefix not set"
	if err := runGazelle(dir, nil); err == nil {
		t.Fatalf("got success; want %q", want)
	} else if !strings.Contains(err.Error(), want) {
		t.Fatalf("got %q; want %q", err, want)
	}
}

// TestSelectLabelsSorted checks that string lists in srcs and deps are sorted
// using buildifier order, even if they are inside select expressions.
// This applies to both new and existing lists and should preserve comments.
// buildifier does not do this yet bazelbuild/buildtools#122, so we do this
// in addition to calling build.Rewrite.
func TestSelectLabelsSorted(t *testing.T) {
	dir, err := createFiles([]fileSpec{
		{path: "WORKSPACE"},
		{
			path: "BUILD",
			content: `
load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_prefix")

go_prefix("example.com/foo")

go_library(
    name = "go_default_library",
    srcs = select({
        "@io_bazel_rules_go//go/platform:linux_amd64": [
						# top comment
						"foo.go",  # side comment
						# bar comment
						"bar.go",
        ],
        "//conditions:default": [],
    }),
)
`,
		},
		{
			path: "foo.go",
			content: `
// +build linux

package foo

import (
    _ "example.com/foo/outer"
    _ "example.com/foo/outer/inner"
    _ "github.com/jr_hacker/tools"
)
`,
		},
		{
			path: "bar.go",
			content: `// +build linux

package foo
`,
		},
		{path: "outer/outer.go", content: "package outer"},
		{path: "outer/inner/inner.go", content: "package inner"},
	})
	want := `load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_prefix")

go_prefix("example.com/foo")

go_library(
    name = "go_default_library",
    srcs = select({
        "@io_bazel_rules_go//go/platform:linux_amd64": [
            # top comment
            # bar comment
            "bar.go",
            "foo.go",  # side comment
        ],
        "//conditions:default": [],
    }),
    visibility = ["//visibility:public"],
    deps = select({
        "@io_bazel_rules_go//go/platform:linux_amd64": [
            "//outer:go_default_library",
            "//outer/inner:go_default_library",
            "@com_github_jr_hacker_tools//:go_default_library",
        ],
        "//conditions:default": [],
    }),
)
`
	if err != nil {
		t.Fatal(err)
	}

	if err := runGazelle(dir, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := ioutil.ReadFile(filepath.Join(dir, "BUILD")); err != nil {
		t.Fatal(err)
	} else if string(got) != want {
		t.Fatalf("got %s ; want %s", string(got), want)
	}
}

func TestFixAndUpdateChanges(t *testing.T) {
	files := []fileSpec{
		{path: "WORKSPACE"},
		{
			path: "BUILD",
			content: `load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_prefix")
load("@io_bazel_rules_go//go:def.bzl", "cgo_library", "go_test")

go_prefix("example.com/foo")

go_library(
    name = "go_default_library",
    srcs = [
        "extra.go",
        "pure.go",
    ],
    library = ":cgo_default_library",
    visibility = ["//visibility:default"],
)

cgo_library(
    name = "cgo_default_library",
    srcs = ["cgo.go"],
)
`,
		},
		{
			path:    "pure.go",
			content: "package foo",
		},
		{
			path: "cgo.go",
			content: `package foo

import "C"
`,
		},
	}

	cases := []struct {
		cmd, want string
	}{
		{
			cmd: "update",
			want: `load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_prefix")
load("@io_bazel_rules_go//go:def.bzl", "cgo_library", "go_test")

go_prefix("example.com/foo")

go_library(
    name = "go_default_library",
    srcs = [
        "cgo.go",
        "pure.go",
    ],
    cgo = True,
    visibility = ["//visibility:default"],
)

cgo_library(
    name = "cgo_default_library",
    srcs = ["cgo.go"],
)
`,
		}, {
			cmd: "fix",
			want: `load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_prefix")

go_prefix("example.com/foo")

go_library(
    name = "go_default_library",
    srcs = [
        "cgo.go",
        "pure.go",
    ],
    cgo = True,
    visibility = ["//visibility:default"],
)
`,
		},
	}

	for _, c := range cases {
		t.Run(c.cmd, func(t *testing.T) {
			dir, err := createFiles(files)
			if err != nil {
				t.Fatal(err)
			}

			if err := runGazelle(dir, []string{c.cmd}); err != nil {
				t.Fatal(err)
			}
			if got, err := ioutil.ReadFile(filepath.Join(dir, "BUILD")); err != nil {
				t.Fatal(err)
			} else if string(got) != c.want {
				t.Fatalf("got %s ; want %s", string(got), c.want)
			}
		})
	}
}

func TestFixUnlinkedCgoLibrary(t *testing.T) {
	files := []fileSpec{
		{path: "WORKSPACE"},
		{
			path: "BUILD",
			content: `load("@io_bazel_rules_go//go:def.bzl", "cgo_library", "go_library", "go_prefix")

go_prefix("example.com/foo")

cgo_library(
    name = "cgo_default_library",
    srcs = ["cgo.go"],
)

go_library(
    name = "go_default_library",
    srcs = ["pure.go"],
    visibility = ["//visibility:public"],
)
`,
		}, {
			path:    "pure.go",
			content: "package foo",
		},
	}

	dir, err := createFiles(files)
	if err != nil {
		t.Fatal(err)
	}

	want := `load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_prefix")

go_prefix("example.com/foo")

go_library(
    name = "go_default_library",
    srcs = ["pure.go"],
    visibility = ["//visibility:public"],
)
`
	if err := runGazelle(dir, []string{"fix"}); err != nil {
		t.Fatal(err)
	}
	if got, err := ioutil.ReadFile(filepath.Join(dir, "BUILD")); err != nil {
		t.Fatal(err)
	} else if string(got) != want {
		t.Fatalf("got %s ; want %s", string(got), want)
	}
}

func TestMultipleDirectories(t *testing.T) {
	files := []fileSpec{
		{path: "WORKSPACE"},
		{
			path:    "a/a.go",
			content: "package a",
		}, {
			path:    "b/b.go",
			content: "package b",
		},
	}
	dir, err := createFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	args := []string{"-go_prefix", "example.com/foo", "a", "b"}
	if err := runGazelle(dir, args); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"a", "b"} {
		path := filepath.Join(dir, d, config.DefaultValidBuildFileNames[0])
		if _, err := os.Stat(path); err != nil {
			t.Errorf("directory %s not visited: %v", d, err)
		}
	}
}

func TestErrorOutsideWorkspace(t *testing.T) {
	files := []fileSpec{
		{path: "a/"},
		{path: "b/"},
	}
	dir, err := createFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	cases := []struct {
		name, dir, want string
		args            []string
	}{
		{
			name: "outside workspace",
			dir:  dir,
			args: nil,
			want: "WORKSPACE cannot be found",
		}, {
			name: "outside repo_root",
			dir:  filepath.Join(dir, "a"),
			args: []string{"-repo_root", filepath.Join(dir, "b")},
			want: "not a subdirectory of repo root",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := runGazelle(c.dir, c.args); err == nil {
				t.Fatal("got success; want %q", c.want)
			} else if !strings.Contains(err.Error(), c.want) {
				t.Fatal("got %q; want %q", err, c.want)
			}
		})
	}
}

func TestBuildFileNameIgnoresBuild(t *testing.T) {
	files := []fileSpec{
		{path: "WORKSPACE"},
		{path: "BUILD/"},
		{
			path:    "a/BUILD",
			content: "!!! parse error",
		}, {
			path:    "a.go",
			content: "package a",
		},
	}
	dir, err := createFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dir)

	args := []string{"-go_prefix", "example.com/foo", "-build_file_name", "BUILD.bazel"}
	if err := runGazelle(dir, args); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "BUILD.bazel")); err != nil {
		t.Errorf("BUILD.bazel not created: %v", err)
	}
}

func TestExternalVendor(t *testing.T) {
	files := []fileSpec{
		{path: "WORKSPACE"},
		{
			path: "a.go",
			content: `package foo

import _ "golang.org/x/bar"
`,
		}, {
			path: "vendor/golang.org/x/bar/bar.go",
			content: `package bar

import _ "golang.org/x/baz"
`,
		}, {
			path:    "vendor/golang.org/x/baz/baz.go",
			content: "package baz",
		},
	}
	dir, err := createFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	args := []string{"-go_prefix", "example.com/foo", "-external", "vendored"}
	if err := runGazelle(dir, args); err != nil {
		t.Fatal(err)
	}

	checkFiles(t, dir, []fileSpec{
		{
			path: config.DefaultValidBuildFileNames[0],
			content: `load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_prefix")

go_prefix("example.com/foo")

go_library(
    name = "go_default_library",
    srcs = ["a.go"],
    visibility = ["//visibility:public"],
    deps = ["//vendor/golang.org/x/bar:go_default_library"],
)
`,
		}, {
			path: "vendor/golang.org/x/bar/" + config.DefaultValidBuildFileNames[0],
			content: `load("@io_bazel_rules_go//go:def.bzl", "go_library")

go_library(
    name = "go_default_library",
    srcs = ["bar.go"],
    visibility = ["//visibility:public"],
    deps = ["//vendor/golang.org/x/baz:go_default_library"],
)
`,
		},
	})
}

// TODO(jayconrod): more tests
//   run in fix mode in testdata directories to create new files
//   run in diff mode in testdata directories to update existing files (no change)
