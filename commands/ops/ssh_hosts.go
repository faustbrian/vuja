package ops

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/faustbrian/vuja/internal/health"
	"github.com/faustbrian/vuja/spec"
	"golang.org/x/sys/unix"
)

const (
	sshConfigFiles      = 128
	sshConfigDepth      = 16
	sshConfigBytes      = 2 << 20
	sshFileBytes        = 256 << 10
	sshGlobDirectories  = 128
	sshDirectoryEntries = 4096
	sshHostLimit        = 2048
)

type sshConfigRoot struct {
	path, base string
	user       bool
}

type sshEnvironmentValue struct {
	value   string
	present bool
}

// One bounded inventory is shared by all consumers. The outer spec cache keeps
// discovery off typing; dependencies are checked when its background refresh runs.
type sshHostInventory struct {
	sync.Mutex
	home    string
	roots   []sshConfigRoot
	files   map[string]os.FileInfo
	env     map[string]sshEnvironmentValue
	results []spec.Suggestion
}

var sshHosts sshHostInventory

func (c *sshHostInventory) load(ctx context.Context, home string, roots []sshConfigRoot) []spec.Suggestion {
	started := time.Now()
	attempted := false
	var outcomeErr error
	// Observe after unlocking: session observers must not inherit inventory
	// ownership. Reusing a cached snapshot is not another acquisition attempt.
	defer func() {
		if attempted {
			health.Report(ctx, health.SSHHosts, time.Since(started), outcomeErr)
		}
	}()
	c.Lock()
	defer c.Unlock()
	if ctx.Err() != nil {
		attempted, outcomeErr = true, ctx.Err()
		return nil
	}
	valid := c.files != nil && c.home == home && slices.Equal(c.roots, roots)
	for path, old := range c.files {
		if !valid || ctx.Err() != nil {
			break
		}
		current, statErr := os.Stat(path)
		valid = sameSSHFile(old, current) && (statErr == nil || errors.Is(statErr, os.ErrNotExist))
	}
	for key, value := range c.env {
		current, present := os.LookupEnv(key)
		valid = valid && value == (sshEnvironmentValue{current, present})
	}
	if ctx.Err() != nil {
		attempted, outcomeErr = true, ctx.Err()
		return nil
	}
	if valid {
		return slices.Clone(c.results)
	}
	attempted = true
	p := sshHostParser{ctx: ctx, home: home, files: make(map[string]os.FileInfo), env: make(map[string]sshEnvironmentValue), requested: make(map[string]bool), visited: make(map[string]bool), hosts: make(map[string]bool)}
	for _, root := range roots {
		if ctx.Err() != nil {
			break
		}
		// The finite configured roots are optional. An absent second root
		// must not turn an otherwise complete exact-cap graph into a failure;
		// retain its absence as a dependency so creation invalidates the cache.
		if _, err := os.Stat(root.path); errors.Is(err, os.ErrNotExist) {
			p.files[root.path] = nil
			continue
		}
		p.read(root.path, root, 0)
	}
	outcomeErr = p.err
	if outcomeErr == nil && p.readFiles == 0 {
		outcomeErr = os.ErrNotExist
	}
	if ctx.Err() != nil {
		return nil
	}
	c.home, c.roots, c.files, c.env, c.results = home, slices.Clone(roots), p.files, p.env, p.results
	if p.unstable || p.err != nil {
		c.files = nil
	}
	return slices.Clone(p.results)
}

func sameSSHFile(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	// Change-time detects edits with restored mtime. Do not compare access-time:
	// reading the configuration may change it without changing its contents.
	return os.SameFile(a, b) && a.ModTime() == b.ModTime() && a.Size() == b.Size() && a.Mode() == b.Mode() && reflect.DeepEqual(sshChangeTime(a), sshChangeTime(b))
}

func sshChangeTime(info os.FileInfo) any {
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if value.IsValid() && value.Kind() == reflect.Struct {
		// Darwin/BSD and Linux expose different names for the same stat field.
		for _, name := range []string{"Ctimespec", "Ctim"} {
			if field := value.FieldByName(name); field.IsValid() && field.CanInterface() {
				return field.Interface()
			}
		}
	}
	return info.Sys()
}

type sshHostParser struct {
	ctx                       context.Context
	home                      string
	files                     map[string]os.FileInfo
	env                       map[string]sshEnvironmentValue
	requested, visited, hosts map[string]bool
	results                   []spec.Suggestion
	bytes, directories        int
	unstable                  bool
	readFiles                 int
	err                       error
}

var errSSHConfigLimit = errors.New("SSH discovery limit reached")
var errSSHConfigType = errors.New("SSH configuration is not a regular file or directory")

func (p *sshHostParser) fail(err error) {
	if err != nil && !errors.Is(err, os.ErrNotExist) && p.err == nil {
		p.err = err
	}
}

func (p *sshHostParser) read(path string, root sshConfigRoot, depth int) {
	if p.ctx.Err() != nil {
		return
	}
	identity := root.base + "\x00" + strconv.FormatBool(root.user)
	request := path + "\x00" + identity
	if p.requested[request] {
		return
	}
	if depth >= sshConfigDepth || len(p.requested) >= sshConfigFiles || p.bytes >= sshConfigBytes {
		p.fail(errSSHConfigLimit)
		return
	}
	p.requested[request] = true
	// Every lexical alias remains a dependency even when its current target has
	// already been parsed. Retargeting a duplicate symlink must invalidate us.
	before, statErr := os.Stat(path)
	p.fail(statErr)
	p.files[path] = before
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		canonical = path
	}
	canonical += "\x00" + identity
	if p.visited[canonical] {
		return
	}
	p.visited[canonical] = true
	if before == nil {
		return
	}
	if !before.Mode().IsRegular() {
		p.fail(errSSHConfigType)
		return
	}
	if before.Size() > sshFileBytes {
		p.fail(errSSHConfigLimit)
		return
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		p.fail(err)
		return
	}
	f := os.NewFile(uintptr(fd), "SSH configuration")
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		p.fail(err)
		return
	}
	if !info.Mode().IsRegular() {
		p.fail(errSSHConfigType)
		return
	}
	if info.Size() > sshFileBytes {
		p.fail(errSSHConfigLimit)
		return
	}
	budget := min(sshFileBytes, sshConfigBytes-p.bytes)
	data, after, err := readSSHConfigSnapshot(f, info, budget)
	p.bytes += len(data)
	if err != nil {
		p.fail(err)
		p.unstable = p.unstable || errors.Is(err, errSSHConfigUnstable)
		return
	}
	p.readFiles++
	p.files[path] = after
	for _, line := range strings.Split(string(data), "\n") {
		if p.ctx.Err() != nil {
			return
		}
		parts := sshConfigFields(line)
		if len(parts) < 2 {
			continue
		}
		switch strings.ToLower(parts[0]) {
		case "host":
			for _, host := range parts[1:] {
				if !safeSSHHost(host) || p.hosts[host] {
					continue
				}
				if len(p.results) >= sshHostLimit {
					p.fail(errSSHConfigLimit)
					continue
				}
				p.hosts[host] = true
				p.results = append(p.results, spec.Suggestion{Cmd: host, Desc: "ssh host"})
			}
		case "include":
			for _, value := range parts[1:] {
				pattern, ok := p.expand(value, root)
				if !ok {
					continue
				}
				for _, match := range p.glob(pattern) {
					p.read(match, root, depth+1)
				}
			}
		}
	}
}

var errSSHConfigUnstable = errors.New("SSH configuration changed during read")

// Keep acquisition and its stability check together: a parsed snapshot must
// never be published under metadata belonging to different file contents.
func readSSHConfigSnapshot(f interface {
	io.Reader
	Stat() (os.FileInfo, error)
}, before os.FileInfo, budget int) ([]byte, os.FileInfo, error) {
	data, err := io.ReadAll(io.LimitReader(f, int64(budget)+1))
	if err != nil {
		return data, nil, err
	}
	if len(data) > budget {
		return data, nil, io.ErrShortBuffer
	}
	after, err := f.Stat()
	if err != nil || !sameSSHFile(before, after) {
		return data, nil, errSSHConfigUnstable
	}
	return data, after, nil
}

// Discover literal aliases, including in conditional files; do not evaluate
// Host/Match applicability or execute Match exec. This is not connection config.
func safeSSHHost(host string) bool {
	if host == "" || len(host) > 255 || strings.HasPrefix(host, "-") {
		return false
	}
	for _, r := range host {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._:-", r) {
			return false
		}
	}
	return true
}

func sshConfigFields(line string) []string {
	var fields []string
	var word strings.Builder
	var quote rune
	flush := func() {
		if word.Len() > 0 {
			fields = append(fields, word.String())
			word.Reset()
		}
	}
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && i+1 < len(runes) {
			next := runes[i+1]
			if next == '\\' || next == '"' || next == '\'' || quote == 0 && next == ' ' {
				i++
				word.WriteRune(next)
			} else {
				word.WriteRune(r)
			} // preserve glob escapes, as OpenSSH does
			continue
		}
		if r == quote && quote != 0 {
			quote = 0
			continue
		}
		if quote == 0 && (r == '"' || r == '\'') {
			quote = r
			continue
		}
		if quote == 0 && r == '#' {
			break
		}
		if quote == 0 && (unicode.IsSpace(r) || r == '=' && len(fields) == 0) {
			flush()
			continue
		}
		// '=' may also be separated by whitespace from the keyword.
		if quote == 0 && r == '=' && len(fields) == 1 && word.Len() == 0 {
			continue
		}
		word.WriteRune(r)
	}
	if quote != 0 {
		return nil
	}
	flush()
	return fields
}

func (p *sshHostParser) expand(value string, root sshConfigRoot) (string, bool) {
	if strings.HasPrefix(value, "~") {
		if !root.user || value != "~" && !strings.HasPrefix(value, "~/") {
			return "", false
		}
		value = p.home + strings.TrimPrefix(value, "~")
	}
	var expanded strings.Builder
	for i := 0; i < len(value); i++ {
		switch {
		case value[i] == '%' && i+1 < len(value):
			i++
			switch value[i] {
			case '%':
				expanded.WriteByte('%')
			case 'd':
				expanded.WriteString(p.home)
			case 'i':
				expanded.WriteString(strconv.Itoa(os.Getuid()))
			default:
				return "", false // connection-dependent tokens are not guessed
			}
		case value[i] == '$' && i+1 < len(value) && value[i+1] == '{':
			end := strings.IndexByte(value[i+2:], '}')
			if end < 0 {
				return "", false
			}
			key := value[i+2 : i+2+end]
			if key == "" {
				return "", false
			}
			for _, r := range key {
				if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
					return "", false
				}
			}
			content, exists := os.LookupEnv(key)
			p.env[key] = sshEnvironmentValue{content, exists}
			if !exists {
				return "", false
			}
			expanded.WriteString(content)
			i += end + 2
		case value[i] == '$' || value[i] == '%':
			return "", false
		default:
			expanded.WriteByte(value[i])
		}
	}
	value = expanded.String()
	if value == "" || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", false
	}
	if !filepath.IsAbs(value) {
		value = sshAppendPath(root.base, value)
	}
	return value, true
}

// Preserve filesystem traversal order. Lexically cleaning link/.. before the
// kernel resolves link would choose the lexical parent, not the target's parent.
func sshAppendPath(base, part string) string {
	return strings.TrimSuffix(base, string(filepath.Separator)) + string(filepath.Separator) + part
}

// Unlike filepath.Glob, this bounds directory allocations and traversal too.
func (p *sshHostParser) glob(pattern string) []string {
	if len(pattern) > 4096 {
		p.fail(errSSHConfigLimit)
		return nil
	}
	var walk func(string, []string) []string
	walk = func(base string, parts []string) []string {
		if p.ctx.Err() != nil {
			return nil
		}
		if len(p.files) >= sshConfigFiles+sshGlobDirectories {
			p.fail(errSSHConfigLimit)
			return nil
		}
		if len(parts) == 0 {
			return []string{base}
		}
		part := parts[0]
		if !strings.ContainsAny(part, "*?[\\") {
			return walk(sshAppendPath(base, part), parts[1:])
		}
		if p.directories >= sshGlobDirectories {
			p.fail(errSSHConfigLimit)
			return nil
		}
		p.directories++
		info, statErr := os.Stat(base)
		p.fail(statErr)
		p.files[base] = info
		if info == nil {
			return nil
		}
		if !info.IsDir() {
			p.fail(errSSHConfigType)
			return nil
		}
		f, err := os.Open(base)
		if err != nil {
			p.fail(err)
			return nil
		}
		entries, err := f.ReadDir(sshDirectoryEntries + 1)
		f.Close()
		if err != nil && err != io.EOF {
			p.fail(err)
			return nil
		}
		if len(entries) > sshDirectoryEntries {
			p.fail(errSSHConfigLimit)
			return nil
		}
		slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		matcher, err := sshGlobMatcher(part)
		if err != nil {
			p.fail(err)
			return nil
		}
		var results []string
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") && !strings.HasPrefix(part, ".") && !strings.HasPrefix(part, "\\.") {
				continue
			}
			if matcher.MatchString(entry.Name()) {
				results = append(results, walk(sshAppendPath(base, entry.Name()), parts[1:])...)
			}
			if len(results) > sshConfigFiles {
				p.fail(errSSHConfigLimit)
				return results[:sshConfigFiles]
			}
		}
		return results
	}
	return walk(string(filepath.Separator), strings.Split(strings.TrimPrefix(pattern, string(filepath.Separator)), string(filepath.Separator)))
}

// Translate POSIX filename globs, not Go's different bracket-negation syntax.
// Named classes use the portable ASCII definitions accepted by RE2. No shell is
// involved; unsupported/malformed classes fail closed rather than broadening.
func sshGlobMatcher(part string) (*regexp.Regexp, error) {
	var pattern strings.Builder
	pattern.WriteString("(?s)^")
	runes := []rune(part)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '\\':
			i++
			if i == len(runes) {
				return nil, filepath.ErrBadPattern
			}
			pattern.WriteString(regexp.QuoteMeta(string(runes[i])))
		case '*':
			pattern.WriteString(".*")
		case '?':
			pattern.WriteByte('.')
		case '[':
			pattern.WriteByte('[')
			i++
			if i == len(runes) {
				return nil, filepath.ErrBadPattern
			}
			if runes[i] == '!' {
				pattern.WriteByte('^')
				i++
			}
			start := i
			if i < len(runes) && runes[i] == ']' {
				pattern.WriteString("\\]")
				i++
			}
			for i < len(runes) && runes[i] != ']' {
				r := runes[i]
				if r == '[' && i+1 < len(runes) && runes[i+1] == ':' {
					end := i + 2
					for end+1 < len(runes) && !(runes[end] == ':' && runes[end+1] == ']') {
						end++
					}
					if end+1 >= len(runes) {
						return nil, filepath.ErrBadPattern
					}
					pattern.WriteString(string(runes[i : end+2]))
					i = end + 2
					continue
				}
				if r == '[' && i+1 < len(runes) && (runes[i+1] == '.' || runes[i+1] == '=') {
					return nil, filepath.ErrBadPattern
				}
				if r == '\\' {
					i++
					if i == len(runes) {
						return nil, filepath.ErrBadPattern
					}
					if strings.ContainsRune("\\]^[-", runes[i]) {
						pattern.WriteByte('\\')
					}
					pattern.WriteRune(runes[i])
				} else if r == '^' || r == '[' || r == '-' && (i == start || i+1 < len(runes) && runes[i+1] == ']') {
					pattern.WriteByte('\\')
					pattern.WriteRune(r)
				} else {
					pattern.WriteRune(r)
				}
				i++
			}
			if i == len(runes) {
				return nil, filepath.ErrBadPattern
			}
			pattern.WriteByte(']')
		default:
			pattern.WriteString(regexp.QuoteMeta(string(runes[i])))
		}
	}
	pattern.WriteByte('$')
	return regexp.Compile(pattern.String())
}
