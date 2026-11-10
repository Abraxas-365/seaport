package hosting

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Home limits: a harness home (the environment's ~/.claude or ~/.rness
// config) is baked into the image, so it is bounded like any build input.
const (
	MaxHomeFiles = 2000
	MaxHomeFile  = 5 << 20
	MaxHomeBytes = 20 << 20
	// MaxHomeUpload bounds an upload (a tar, maybe gzipped).
	MaxHomeUpload = 25 << 20
	MaxHomePath   = 255
	MaxHomeDepth  = 12
	// MaxHomes bounds an organization's homes (unused ones are swept).
	MaxHomes = 500
	// HomeGrace: a home no environment version names is deleted after it
	// (time to name it after uploading).
	HomeGrace = 24 * time.Hour
	// MaxHomeSweep bounds the homes one sweep deletes.
	MaxHomeSweep = 100
)

// ValidHomeDigest tells whether d names a home (sha256:<hex>).
func ValidHomeDigest(d string) bool { return homeDigest.MatchString(d) }

// HomeFilePath checks a path asked of a home (GET …/files/{path}).
func HomeFilePath(rel string) error {
	if rel == "" || len(rel) > MaxHomePath || path.IsAbs(rel) || path.Clean(rel) != rel || strings.HasPrefix(rel, "../") {
		return errx.InvalidField("path", "path must be a file of the home")
	}
	return nil
}

// homeDigest is how a spec names a home: sha256 of its harness and
// normalized tar (HomeBundle).
var homeDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// HomeRoot is the dir of the home a harness reads its user config from
// (relative to /home/agent); a home's paths are relative to it.
func HomeRoot(harness string) string {
	switch harness {
	case HarnessRness:
		return ".rness"
	case HarnessClaudeCode:
		return ".claude"
	}
	return ""
}

// homeTop are the top-level names a home may hold, per harness: true for
// a dir, false for a file. Everything else (sessions, history, caches,
// sign-in, personal memory, plugin installs) is the harness's own state
// or the owner's: never baked into an image.
var homeTop = map[string]map[string]bool{
	HarnessClaudeCode: {
		"CLAUDE.md": false, "settings.json": false, "keybindings.json": false,
		"agents": true, "commands": true, "skills": true, "hooks": true, "output-styles": true,
	},
	HarnessRness: {
		"init.lua": false, "hooks.json": false,
		"lua": true, "plugins": true, "skills": true,
	},
}

// homeTopFile: a top-level file a home may hold besides homeTop's (a
// status line script settings.json points at).
func homeTopFile(harness, name string) bool {
	return harness == HarnessClaudeCode && strings.HasPrefix(name, "statusline")
}

// HomeDir tells whether a dir of a home (relative to HomeRoot) may hold
// files to upload, so a walk skips the rest (sessions can be gigabytes).
func HomeDir(harness, rel string) bool {
	parts := strings.Split(rel, "/")
	if slices.Contains(parts, ".git") {
		return false
	}
	return homeTop[harness][parts[0]]
}

// HomeRules is what a client needs to pick a harness home's files before
// uploading (the web filters a dropped folder with it; the port still
// checks every path with HomePath).
type HomeRules struct {
	// Root is the dir of /home/agent the home is (".claude", ".rness").
	Root string `json:"root"`
	// Dirs and Files are the top-level names a home may hold;
	// FilePrefixes, top-level files by prefix (statusline scripts).
	Dirs         []string `json:"dirs"`
	Files        []string `json:"files"`
	FilePrefixes []string `json:"file_prefixes"`
	// Reserved are paths Seaport owns (never uploaded).
	Reserved []string `json:"reserved"`
	// Secrets are the patterns ScanSecrets refuses (RE2 that JavaScript
	// reads the same), so a client warns before uploading.
	Secrets []SecretPattern `json:"secrets"`
	// Limits of a home (bytes) and of one upload.
	MaxFiles  int `json:"max_files"`
	MaxFile   int `json:"max_file"`
	MaxBytes  int `json:"max_bytes"`
	MaxUpload int `json:"max_upload"`
	MaxDepth  int `json:"max_depth"`
}

// HomeRulesOf answers each harness's HomeRules.
func HomeRulesOf() map[string]HomeRules {
	out := map[string]HomeRules{}
	for harness, tops := range homeTop {
		r := HomeRules{Root: HomeRoot(harness), Dirs: []string{}, Files: []string{}, FilePrefixes: []string{},
			Reserved: []string{}, Secrets: make([]SecretPattern, 0, len(secretPatterns)), MaxFiles: MaxHomeFiles,
			MaxFile: MaxHomeFile, MaxBytes: MaxHomeBytes, MaxUpload: MaxHomeUpload, MaxDepth: MaxHomeDepth}
		for _, p := range secretPatterns {
			r.Secrets = append(r.Secrets, SecretPattern{What: p.what, Pattern: p.re.String()})
		}
		for name, dir := range tops {
			if dir {
				r.Dirs = append(r.Dirs, name)
			} else {
				r.Files = append(r.Files, name)
			}
		}
		if harness == HarnessClaudeCode {
			r.FilePrefixes = append(r.FilePrefixes, "statusline") // homeTopFile
		}
		for p := range homeReserved[harness] {
			r.Reserved = append(r.Reserved, p)
		}
		slices.Sort(r.Dirs)
		slices.Sort(r.Files)
		slices.Sort(r.Reserved)
		out[harness] = r
	}
	return out
}

// homeReserved are paths Seaport owns in a harness's home.
var homeReserved = map[string]map[string]string{
	HarnessRness: {"lua/seaport.lua": "Seaport's (it routes models through the port's gateway)"},
}

// HomePath checks one path of a home (relative to HomeRoot): nil when it
// may be uploaded, else why not. The CLI filters with it before reading.
func HomePath(harness, rel string) error {
	tops, ok := homeTop[harness]
	if !ok {
		return errx.InvalidField("harness", "harness must be rness or claude-code")
	}
	if rel == "" || len(rel) > MaxHomePath || strings.ContainsAny(rel, "\x00\\") || !utf8.ValidString(rel) ||
		path.IsAbs(rel) || path.Clean(rel) != rel || rel == "." {
		return errx.Validation(strconv.Quote(rel) + " is not a relative path (no .., //, \\ or absolute paths)")
	}
	parts := strings.Split(rel, "/")
	if len(parts) > MaxHomeDepth {
		return errx.Validation(rel + " is nested deeper than " + strconv.Itoa(MaxHomeDepth) + " levels")
	}
	for _, p := range parts {
		switch {
		case p == "..":
			return errx.Validation(strconv.Quote(rel) + " is not a relative path (no .., //, \\ or absolute paths)")
		case p == ".git":
			return errx.Validation(rel + ": git metadata is not config")
		}
	}
	base := parts[len(parts)-1]
	switch {
	case base == ".credentials.json" || strings.HasPrefix(base, "credentials.json"):
		return errx.Validation(rel + ": credentials never go in a home (slots sign in, or use a model key)")
	case base == "settings.local.json":
		return errx.Validation(rel + ": local settings are one machine's")
	case base == ".DS_Store" || strings.HasSuffix(base, "~") || strings.HasSuffix(base, ".bak") ||
		strings.Contains(base, ".backup"):
		return errx.Validation(rel + ": a backup or editor leftover")
	}
	if harness == HarnessClaudeCode && parts[0] == "plugins" {
		return errx.Validation(rel + ": plugins aren't uploaded; enable them in settings.json (enabledPlugins, " +
			"extraKnownMarketplaces) and the build installs them")
	}
	dir, known := tops[parts[0]]
	if !known && len(parts) == 1 && homeTopFile(harness, parts[0]) {
		known = true
	}
	if !known {
		return errx.Validation(rel + ": not harness config (a " + harness + " home holds " + strings.Join(homeNames(harness), ", ") + ")")
	}
	if !dir && len(parts) > 1 {
		return errx.Validation(rel + ": " + parts[0] + " is a file")
	}
	if dir && len(parts) == 1 {
		return errx.Validation(rel + ": " + parts[0] + " is a directory")
	}
	if why, ok := homeReserved[harness][rel]; ok {
		return errx.Validation(rel + " is " + why)
	}
	return nil
}

// homeNames lists a harness's home top-level names (dirs with a slash).
func homeNames(harness string) []string {
	var out []string
	for n, dir := range homeTop[harness] {
		if dir {
			n += "/"
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// HomeFile is one file of a home.
type HomeFile struct {
	// Path is relative to the harness's HomeRoot.
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Exec: the file is executable (hooks, scripts).
	Exec bool `json:"exec,omitempty"`
}

// Home is a harness home uploaded to an organization: content-addressed
// (Digest), stored sealed in blob storage, named by specs' home. One no
// environment version names is deleted after HomeGrace.
type Home struct {
	Organization identity.OrganizationID `json:"-"`
	Digest       string                  `json:"digest"`
	Harness      string                  `json:"harness"`
	Files        []HomeFile              `json:"files"`
	// Size is the files' total size.
	Size      int64     `json:"size"`
	Key       string    `json:"-"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// UploadHome is the input of Commands.UploadHome: a tar (maybe gzipped)
// of the home's files, paths relative to the harness's HomeRoot.
type UploadHome struct {
	Harness string
	Archive []byte
	// CreatedBy is filled from the caller (not client input).
	CreatedBy string
}

func (u UploadHome) Validate() error {
	if HomeRoot(u.Harness) == "" {
		return errx.InvalidField("harness", "harness must be rness or claude-code")
	}
	if len(u.Archive) == 0 {
		return errx.Validation("the home is empty: upload a tar of its files")
	}
	if len(u.Archive) > MaxHomeUpload {
		return errx.PayloadTooLarge("the home upload is too large (max " + strconv.Itoa(MaxHomeUpload>>20) + " MiB)")
	}
	return text.Optional("created_by", u.CreatedBy, text.MaxName)
}

// HomeBundle is a validated home: its manifest and normalized tar (sorted
// paths, uid/gid 1000, mode 0644 or 0755, mtime 0); its digest is the
// SHA-256 of the harness and that tar, so the same files always make the
// same home.
type HomeBundle struct {
	Digest string
	Files  []HomeFile
	Size   int64
	Tar    []byte
}

// ReadHome validates an uploaded archive (tar, or tar.gz) as a home for
// harness: allowed paths only (HomePath), regular files only, within the
// limits, no secret-looking strings; and normalizes it.
func ReadHome(harness string, archive []byte) (HomeBundle, error) {
	var r io.Reader = bytes.NewReader(archive)
	if len(archive) >= 2 && archive[0] == 0x1f && archive[1] == 0x8b {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return HomeBundle{}, errx.Validation("the home is not a valid tar.gz")
		}
		r = gz
	}
	tr := tar.NewReader(r)
	files := map[string][]byte{}
	exec := map[string]bool{}
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return HomeBundle{}, errx.Validation("the home is not a valid tar archive")
		}
		name := strings.TrimPrefix(h.Name, "./")
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		case tar.TypeXGlobalHeader:
			continue
		default:
			return HomeBundle{}, errx.Validation(name + ": only regular files go in a home (no links or devices)")
		}
		if err := HomePath(harness, name); err != nil {
			return HomeBundle{}, err
		}
		if _, dup := files[name]; dup {
			return HomeBundle{}, errx.Validation(name + " is twice in the archive")
		}
		if len(files) >= MaxHomeFiles {
			return HomeBundle{}, errx.Validation("a home holds at most " + strconv.Itoa(MaxHomeFiles) + " files")
		}
		body, err := io.ReadAll(io.LimitReader(tr, MaxHomeFile+1))
		if err != nil {
			return HomeBundle{}, errx.Validation("the home is not a valid tar archive")
		}
		if len(body) > MaxHomeFile {
			return HomeBundle{}, errx.Validation(name + " is larger than " + strconv.Itoa(MaxHomeFile>>20) + " MiB")
		}
		if total += int64(len(body)); total > MaxHomeBytes {
			return HomeBundle{}, errx.Validation("a home holds at most " + strconv.Itoa(MaxHomeBytes>>20) +
				" MiB: push a dir with only what the agent needs (large skill assets can go in the environment's repos)")
		}
		if err := ScanSecrets(name, body); err != nil {
			return HomeBundle{}, err
		}
		files[name] = body
		exec[name] = h.Mode&0o111 != 0
	}
	if len(files) == 0 {
		return HomeBundle{}, errx.Validation("the home is empty: upload a tar of its files")
	}
	// A dir and a file of one name can't both be extracted.
	for name := range files {
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			if _, clash := files[dir]; clash {
				return HomeBundle{}, errx.Validation(dir + " is both a file and a directory")
			}
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	out := HomeBundle{Files: make([]HomeFile, 0, len(names)), Size: total}
	for _, n := range names {
		mode := int64(0o644)
		if exec[n] {
			mode = 0o755
		}
		body := files[n]
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: n, Mode: mode, Size: int64(len(body)),
			Uid: 1000, Gid: 1000, ModTime: time.Unix(0, 0), Format: tar.FormatPAX}); err != nil {
			return HomeBundle{}, errx.Wrap(err, "normalize the home", errx.TypeInternal)
		}
		if _, err := tw.Write(body); err != nil {
			return HomeBundle{}, errx.Wrap(err, "normalize the home", errx.TypeInternal)
		}
		sum := sha256.Sum256(body)
		out.Files = append(out.Files, HomeFile{Path: n, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), Exec: exec[n]})
	}
	if err := tw.Close(); err != nil {
		return HomeBundle{}, errx.Wrap(err, "normalize the home", errx.TypeInternal)
	}
	out.Tar = buf.Bytes()
	// The harness is part of the name: the same files make another home
	// for the other harness (each is checked and built for its own).
	sum := sha256.Sum256(append([]byte(harness+"\x00"), out.Tar...))
	out.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return out, nil
}

// HomeFileData reads one file of a normalized home tar.
func HomeFileData(tarball []byte, name string) ([]byte, error) {
	tr := tar.NewReader(bytes.NewReader(tarball))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errx.NotFound("no file " + name + " in the home")
		}
		if err != nil {
			return nil, errx.Wrap(err, "read the home", errx.TypeInternal)
		}
		if h.Name == name {
			body, err := io.ReadAll(io.LimitReader(tr, MaxHomeFile+1))
			if err != nil {
				return nil, errx.Wrap(err, "read the home", errx.TypeInternal)
			}
			return body, nil
		}
	}
}

// SecretPattern is one of ScanSecrets' patterns, for clients.
type SecretPattern struct {
	What    string `json:"what"`
	Pattern string `json:"pattern"`
}

// secretPatterns are strings that look like credentials (high confidence:
// fixed prefixes of providers' key formats). Clients get them through
// HomeRules, so keep them to syntax RE2 and JavaScript share.
var secretPatterns = []struct {
	what string
	re   *regexp.Regexp
}{
	{"an Anthropic API key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)},
	{"an OpenAI API key", regexp.MustCompile(`sk-(proj-|svcacct-)?[A-Za-z0-9_-]{40,}`)},
	{"a GitHub token", regexp.MustCompile(`(gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{50,})`)},
	{"a GitLab token", regexp.MustCompile(`glpat-[A-Za-z0-9_-]{20,}`)},
	{"a Slack token", regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,}`)},
	{"an AWS access key", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"a Google API key", regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)},
	{"a private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"a Seaport crew token", regexp.MustCompile(`sp_crew_[A-Za-z0-9_-]{16,}`)},
	{"an IAMKit API key", regexp.MustCompile(`ik_key_[A-Za-z0-9_-]{16,}`)},
}

// ScanSecrets refuses a file holding a credential-looking string: a home
// is baked into an image and readable with environments:read. Secrets go
// as the agent's env secrets, which the config reads from the environment.
func ScanSecrets(name string, body []byte) error {
	if !utf8.Valid(body) {
		return nil // binary: images and the like
	}
	for _, p := range secretPatterns {
		if loc := p.re.FindIndex(body); loc != nil {
			line := bytes.Count(body[:loc[0]], []byte("\n")) + 1
			return errx.Validation(name + " line " + strconv.Itoa(line) + " looks like " + p.what +
				": keep secrets out of the home (give the agent an env secret and read it from the environment)")
		}
	}
	return nil
}
