package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/zachjamesgreen/tagshelf/internal/rules"
)

// Keys lists the keys of a config file, in the order of Config's fields.
func Keys() []string { return knownKeys() }

// Values are a config's settings by key: a string, an int or a bool each. A
// setting that is on unless turned off (auto_resolve_exact, cue_rewrite) is
// the bool it comes to.
func (c Config) Values() map[string]any {
	v := reflect.ValueOf(c)
	out := map[string]any{}
	for i, key := range knownKeys() {
		f := v.Field(i)
		if f.Kind() == reflect.Pointer {
			out[key] = f.IsNil() || f.Elem().Bool()
			continue
		}
		out[key] = f.Interface()
	}
	return out
}

// Set returns the config with the setting key given value, which must be of
// the type Values has for it.
func (c Config) Set(key string, value any) (Config, error) {
	v := reflect.ValueOf(&c).Elem()
	for i, k := range knownKeys() {
		if k != key {
			continue
		}
		f := v.Field(i)
		val := reflect.ValueOf(value)
		if f.Kind() == reflect.Pointer {
			b, ok := value.(bool)
			if !ok {
				return c, fmt.Errorf("%s takes true or false, not %v", key, value)
			}
			f.Set(reflect.ValueOf(&b))
			return c, nil
		}
		if !val.IsValid() || val.Type() != f.Type() {
			return c, fmt.Errorf("%s does not take %v", key, value)
		}
		f.Set(val)
		return c, nil
	}
	return c, fmt.Errorf("there is no setting %q", key)
}

// Changed lists the keys whose settings differ between c and other, in the
// order of Keys.
func (c Config) Changed(other Config) []string {
	a, b := c.Values(), other.Values()
	var keys []string
	for _, k := range knownKeys() {
		if a[k] != b[k] {
			keys = append(keys, k)
		}
	}
	return keys
}

// Update returns the text of a config file with the settings in set given
// their new values, each a string, an int or a bool. Nothing else in the
// file changes: comments, the order of the keys and every other line stay as
// they were. A key the file comments out is put back on that line, and one
// it does not have at all is added at the end.
//
// A value written over several lines is not edited: that is an error, and so
// is text that cannot be read back as the settings asked for.
func Update(data []byte, set map[string]any) ([]byte, error) {
	for key := range set {
		if !isKey(key) {
			return nil, fmt.Errorf("there is no setting %q", key)
		}
	}
	eol := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		eol = "\r\n"
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	// The file's last line break, if it has one, is put back at the end.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("the file is not valid YAML: %w", err)
	}
	type pair struct{ key, value *yaml.Node }
	found := map[string]pair{}
	if len(doc.Content) == 1 {
		if doc.Content[0].Kind != yaml.MappingNode {
			return nil, errors.New("the file is not a list of settings")
		}
		pairs := doc.Content[0].Content
		for i := 0; i+1 < len(pairs); i += 2 {
			found[pairs[i].Value] = pair{pairs[i], pairs[i+1]}
		}
	}

	for _, key := range knownKeys() {
		value, ok := set[key]
		if !ok {
			continue
		}
		written, err := scalar(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if p, ok := found[key]; ok {
			i := p.key.Line - 1
			if p.value.Kind != yaml.ScalarNode || i < 0 || i >= len(lines) || (p.value.Line != p.key.Line && p.value.Value != "") ||
				p.value.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
				return nil, fmt.Errorf("the value of %s is written over several lines: change it in the file by hand", key)
			}
			line := lines[i]
			// What follows "key:" on its line: the value and a comment.
			colon := strings.Index(line, ":")
			if colon < 0 {
				return nil, fmt.Errorf("the line of %s could not be read", key)
			}
			rest := line[colon+1:]
			lines[i] = line[:colon+1] + " " + written + afterScalar(rest)
			continue
		}
		if i := commentedLine(lines, key); i >= 0 {
			_, rest, _ := strings.Cut(lines[i], ":")
			lines[i] = key + ": " + written + afterScalar(rest)
			continue
		}
		lines = append(lines, key+": "+written)
	}

	out := []byte(strings.Join(lines, eol) + eol)
	// However the text was edited, it is only good if it reads back as asked.
	var got Config
	if err := parse(out, &got); err != nil {
		return nil, fmt.Errorf("the file could not be changed safely: %w", err)
	}
	read := got.Values()
	for key, value := range set {
		if read[key] != value {
			return nil, fmt.Errorf("the file could not be changed safely: %s read back as %v", key, read[key])
		}
	}
	return out, nil
}

func isKey(key string) bool {
	for _, k := range knownKeys() {
		if k == key {
			return true
		}
	}
	return false
}

// scalar writes a setting's value as YAML: a string in double quotes.
func scalar(value any) (string, error) {
	switch v := value.(type) {
	case bool:
		return strconv.FormatBool(v), nil
	case int:
		return strconv.Itoa(v), nil
	case string:
		var b strings.Builder
		b.WriteByte('"')
		for _, r := range v {
			switch {
			case r == '\\' || r == '"':
				b.WriteByte('\\')
				b.WriteRune(r)
			case unicode.IsControl(r) || r == 0x2028 || r == 0x2029 || r == 0xFEFF:
				return "", errors.New("the value holds a character that cannot be typed")
			default:
				b.WriteRune(r)
			}
		}
		b.WriteByte('"')
		return b.String(), nil
	}
	return "", fmt.Errorf("a setting cannot be %T", value)
}

// afterScalar returns what follows the value in rest, the text after a
// key's colon: the gap and the comment at the end of the line, or nothing.
func afterScalar(rest string) string {
	s := strings.TrimLeft(rest, " \t")
	end := 0
	switch {
	case strings.HasPrefix(s, `"`):
		end = len(s)
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++
			} else if s[i] == '"' {
				end = i + 1
				break
			}
		}
	case strings.HasPrefix(s, "'"):
		end = len(s)
		for i := 1; i < len(s); i++ {
			if s[i] != '\'' {
				continue
			}
			if i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			end = i + 1
			break
		}
	case strings.HasPrefix(s, "#"):
		// No value at all, only a comment.
		return " " + s
	default:
		end = len(s)
		if i := plainEnd.FindStringIndex(s); i != nil {
			end = i[0]
		}
	}
	tail := s[end:]
	if strings.TrimSpace(tail) == "" {
		return ""
	}
	return tail
}

// plainEnd is where a value written without quotes ends before a comment.
var plainEnd = regexp.MustCompile(`[ \t]+#`)

// commentedLine finds the line that comments key out, as in "#untagged: x",
// or -1.
func commentedLine(lines []string, key string) int {
	for i, line := range lines {
		rest, ok := strings.CutPrefix(line, "#")
		if !ok {
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(rest, " "), key+":") {
			return i
		}
	}
	return -1
}

// WriteFile puts data in the config file at path, making its folder if need
// be. The file is written beside itself and moved into place, so that a
// crash leaves the old file or the new one, never half of either.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		os.Remove(name)
	}
	return err
}

// Check finds what is wrong with settings before they are saved, and returns
// the reason by key. It looks only at the settings themselves, not at what
// is on the disk.
func Check(c Config) map[string]string {
	problems := map[string]string{}
	if c.Workers < 0 {
		problems["workers"] = "must be 0 or more; 0 means one for each CPU"
	}
	if msg := contactProblem(c.MusicBrainzContact); msg != "" {
		problems["musicbrainz_contact"] = msg
	}
	if msg := folderNameProblem(c.Compilations); msg != "" {
		problems["compilations"] = msg
	}

	// The folders as a run would use them, defaults filled in.
	if c.Destination == "" {
		return problems
	}
	d := c.WithDefaults()
	same := func(a, b string) bool { return a != "" && b != "" && rules.PathKey(a) == rules.PathKey(b) }
	inside := func(path, dir string) bool {
		rel, err := filepath.Rel(dir, path)
		return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	if c.Source != "" && !same(c.Source, c.Destination) && inside(filepath.Clean(c.Destination), filepath.Clean(c.Source)) {
		problems["destination"] = "is inside the source folder; use the same folder to reorganize it in place, or one outside it"
	}
	sides := []struct{ key, path string }{{"untagged", d.Untagged}, {"other", d.Other}, {"duplicates", d.Duplicates}}
	for i, s := range sides {
		switch {
		case same(s.path, c.Destination):
			problems[s.key] = "is the library folder itself; its files would be taken for the library's"
		case same(s.path, c.Source):
			problems[s.key] = "is the source folder itself"
		}
		for _, t := range sides[:i] {
			if same(s.path, t.path) {
				problems[s.key] = "is the same folder as " + t.key
			}
		}
	}
	return problems
}

// contactProblem says why contact cannot be what MusicBrainz is told to
// reach the user at: it takes an email address or a URL. Empty is fine, and
// leaves lookups off.
func contactProblem(contact string) string {
	contact = strings.TrimSpace(contact)
	if contact == "" {
		return ""
	}
	if strings.ContainsFunc(contact, unicode.IsSpace) {
		return "must be an email address or a URL, without spaces"
	}
	if u, err := url.Parse(contact); err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.Contains(u.Host, ".") {
		return ""
	}
	if name, host, ok := strings.Cut(contact, "@"); ok && name != "" && strings.Contains(host, ".") &&
		!strings.HasPrefix(host, ".") && !strings.HasSuffix(host, ".") && !strings.Contains(host, "@") {
		return ""
	}
	return "must be an email address or a URL starting with https://"
}

// folderNameProblem says why name cannot be a folder's name on Windows.
// Empty is fine: the default is then used.
func folderNameProblem(name string) string {
	switch {
	case name == "":
		return ""
	case strings.ContainsAny(name, `\/:*?"<>|`):
		return `cannot hold any of \ / : * ? " < > |`
	case strings.ContainsFunc(name, unicode.IsControl):
		return "cannot hold a character that cannot be typed"
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") || strings.HasPrefix(name, " "):
		return "cannot begin with a space, or end with a space or a full stop"
	}
	return ""
}
