package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	chromaModule       = "github.com/alecthomas/chroma/v2"
	chromaVersion      = "v2.27.0"
	chromaNoticeRoot   = "third_party/notices/"
	chromaSourceSHA256 = "13654b052f885cb3fa47d7343e13975d9c8a64c3bcd84993c35d5eb01f22b23f"
	chromaObservedSPDX = "OFL-1.1-no-RFN"
)

// Inspect the selected module, including active replacements, with Go's module
// resolver. Read-only offline resolution prevents packaging from repairing drift.
func validateChromaModule(rootPath string) error {
	if info, err := os.Lstat(filepath.Join(rootPath, "go.mod")); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("chroma source go.mod is missing or unsafe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-m", "-json", chromaModule)
	command.Dir = rootPath
	command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	data, err := command.Output()
	if err != nil {
		return errors.New("resolve reviewed chroma module failed")
	}
	var module goListModule
	if err := json.Unmarshal(data, &module); err != nil || module.Path != chromaModule || module.Version != chromaVersion || module.Replace != nil {
		return errors.New("chroma module version or replacement differs from reviewed source")
	}
	return nil
}

func validateChromaBinary(binary *os.File, target string) error {
	info, err := buildinfo.Read(binary)
	if err != nil {
		return errors.New("read CLI Go build information failed")
	}
	found := false
	for _, module := range info.Deps {
		if module.Path == chromaModule {
			if found || module.Version != chromaVersion || module.Replace != nil {
				return errors.New("binary chroma version or replacement differs from reviewed source")
			}
			found = true
		}
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if !found || settings["GOOS"]+"-"+settings["GOARCH"] != target {
		return errors.New("binary chroma dependency or platform does not match distribution")
	}
	return nil
}

type chromaNoticeProof struct {
	SchemaVersion     int      `json:"schema_version"`
	Module            string   `json:"module"`
	Version           string   `json:"version"`
	SourceURL         string   `json:"source_url"`
	SourceSHA256      string   `json:"source_sha256"`
	FontSourceURL     string   `json:"font_source_url"`
	FontEncodedSHA256 string   `json:"font_encoded_sha256"`
	ReviewedLicenses  []string `json:"reviewed_licenses"`
	ReservedFontNames []string `json:"reserved_font_names"`
	CopyrightHolders  []string `json:"copyright_holders"`
	Detector          struct {
		Tool             string   `json:"tool"`
		Version          string   `json:"version"`
		ObservedLicenses []string `json:"observed_licenses"`
	} `json:"detector"`
	ReviewBasis string `json:"review_basis"`
}

func noticeSourceFile(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() == 0 || info.Size() > 1<<20 {
		return nil, fmt.Errorf("invalid retained license source %q", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("retained license source changed")
	}
	return io.ReadAll(io.LimitReader(f, (1<<20)+1))
}

func loadChromaNoticeProof(rootPath string) (map[string][]byte, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	copying, err := noticeSourceFile(root, chromaNoticeRoot+"chroma.COPYING")
	if err != nil {
		return nil, err
	}
	provenance, err := noticeSourceFile(root, chromaNoticeRoot+"chroma.PROVENANCE.json")
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(provenance))
	decoder.DisallowUnknownFields()
	var proof chromaNoticeProof
	if err := decoder.Decode(&proof); err != nil {
		return nil, fmt.Errorf("decode Chroma provenance: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("chroma provenance has trailing data")
	}
	hash := sha256.Sum256(copying)
	if proof.SchemaVersion != 1 || proof.Module != chromaModule || proof.Version != chromaVersion ||
		proof.SourceURL != "https://github.com/alecthomas/chroma/blob/v2.27.0/COPYING" ||
		proof.SourceSHA256 != chromaSourceSHA256 || hex.EncodeToString(hash[:]) != proof.SourceSHA256 ||
		proof.FontSourceURL != "https://github.com/alecthomas/chroma/blob/v2.27.0/formatters/svg/font_liberation_mono.go" ||
		proof.FontEncodedSHA256 != "08a76705aa3a34ece73a65398252e0df807f7671231a0d2a3f6fd4719e851380" {
		return nil, errors.New("chroma license source or digest does not match the reviewed version")
	}
	if !slices.Equal(proof.ReviewedLicenses, []string{"MIT", "OFL-1.1-RFN"}) ||
		!slices.Equal(proof.ReservedFontNames, []string{"Arimo", "Tinos", "Cousine", "Liberation"}) ||
		!slices.Equal(proof.CopyrightHolders, []string{"Google Corporation (2010)", "Red Hat, Inc. (2012)"}) ||
		proof.Detector.Tool != "cyclonedx-gomod" || proof.Detector.Version != "v1.10.0" ||
		!slices.Equal(proof.Detector.ObservedLicenses, []string{chromaObservedSPDX}) || strings.TrimSpace(proof.ReviewBasis) == "" {
		return nil, errors.New("chroma reviewed scopes and observed detector classification must remain distinct")
	}
	return map[string][]byte{"chroma.COPYING": copying, "chroma.PROVENANCE.json": provenance}, nil
}

func licenseBundle(rootPath string) (map[string][]byte, error) {
	if err := validateChromaModule(rootPath); err != nil {
		return nil, err
	}
	files, err := loadChromaNoticeProof(rootPath)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for source, name := range map[string]string{
		"LICENSE": "YHC.LICENSE", "NOTICE": "YHC.NOTICE",
		"internal/webui/assets/vendor/marked.LICENSE.txt": "marked.LICENSE.txt",
		"internal/webui/assets/vendor/marked.NOTICE.txt":  "marked.NOTICE.txt",
	} {
		data, err := noticeSourceFile(root, source)
		if err != nil {
			return nil, err
		}
		files[name] = data
	}
	return files, nil
}

func bundleLicenses(rootPath, output string) error {
	files, err := licenseBundle(rootPath)
	if err != nil {
		return err
	}
	for name, data := range files {
		if err := writeInventory(filepath.Join(output, name), data); err != nil {
			return err
		}
	}
	return nil
}

func packageCLI(rootPath, target, output string) error {
	filename, ok := map[string]string{
		"linux-amd64": "yhc", "darwin-amd64": "yhc", "darwin-arm64": "yhc", "windows-amd64": "yhc.exe",
	}[target]
	if !ok {
		return errors.New("unsupported CLI package target")
	}
	files, err := licenseBundle(rootPath)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.ToSlash(filepath.Join("build", target, filename))
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() == 0 {
		return errors.New("CLI package binary is invalid")
	}
	binary, err := root.Open(name)
	if err != nil {
		return err
	}
	defer binary.Close()
	opened, err := binary.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("CLI package binary changed")
	}
	if err := validateChromaBinary(binary, target); err != nil {
		return err
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	write := func(name string, mode os.FileMode, input io.Reader) error {
		header := &zip.FileHeader{Name: "yhc-" + target + "/" + name, Method: zip.Deflate}
		header.SetMode(mode)
		header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		entry, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = io.Copy(entry, input)
		return err
	}
	if err := write(filename, 0o755, binary); err != nil {
		_ = archive.Close()
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := write("licenses/"+name, 0o644, bytes.NewReader(files[name])); err != nil {
			_ = archive.Close()
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return writeInventory(output, buffer.Bytes())
}
