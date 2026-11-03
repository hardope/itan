// Command notices prints the licenses of the code built into an Itan
// program: Go, the Go modules it uses, and the web player (hls.js).
// Run it from the repository's root:
//
//	go run ./packaging/notices [-goos windows] ./cmd/itan > THIRD-PARTY.txt
//
// -goos lists the modules of another system's build.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	goos := flag.String("goos", "", "the system the program is built for (default: this one)")
	flag.Parse()
	pkg := "./cmd/itan"
	if flag.NArg() > 0 {
		pkg = flag.Arg(0)
	}
	if *goos != "" {
		os.Setenv("GOOS", *goos) // for the go commands this runs
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	fmt.Fprintln(out, "Itan is licensed under the GNU AGPL v3 (LICENSE). It is built from the")
	fmt.Fprintln(out, "following software, under these licenses.")

	section(out, "Go standard library")
	copyFile(out, filepath.Join(goCmd("env", "GOROOT"), "LICENSE"))

	list := goCmd("list", "-deps", "-f",
		"{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}", pkg)
	modules := strings.Split(list, "\n")
	slices.Sort(modules)
	for _, line := range slices.Compact(modules) {
		path, rest, _ := strings.Cut(line, "\t")
		version, dir, _ := strings.Cut(rest, "\t")
		if path == "" {
			continue
		}
		section(out, path+" "+version)
		entries, err := os.ReadDir(dir)
		if err != nil {
			log.Fatal(err)
		}
		found := false
		for _, e := range entries {
			name := strings.ToLower(e.Name())
			if e.IsDir() || !(strings.HasPrefix(name, "licen") || strings.HasPrefix(name, "copying") ||
				strings.HasPrefix(name, "notice") || strings.HasPrefix(name, "patents")) {
				continue
			}
			if found {
				fmt.Fprintf(out, "\n--- %s ---\n\n", e.Name())
			}
			copyFile(out, filepath.Join(dir, e.Name()))
			found = true
		}
		if !found {
			log.Fatalf("%s: no license file in %s", path, dir)
		}
	}

	section(out, "hls.js (in the web UI)")
	copyFile(out, filepath.Join("internal", "server", "web", "vendor", "hls.js.LICENSE"))
}

func section(out *bufio.Writer, title string) {
	fmt.Fprintf(out, "\n================================================================\n%s\n\n", title)
}

func copyFile(out *bufio.Writer, path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	out.Write(b)
}

func goCmd(args ...string) string {
	cmd := exec.Command("go", args...)
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		log.Fatalf("go %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(b))
}
