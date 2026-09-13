package main

import (
	"fmt"
	"sort"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// NamingContract is the part of .goreleaser.yaml that decides what a
// release's files are called.
//
// It is read out of the tag's own .goreleaser.yaml rather than restated
// here, because a hard-coded list of six filenames is exactly the thing
// that goes stale without anyone noticing: it would keep passing after
// the archive matrix or the name template changed underneath it, and the
// first person to find out would be a participant whose `npm install`
// 404s. Reading the contract from its source means a change to the
// contract changes what this expects, or — where the change is one this
// tool does not model — fails here, loudly, before anything is published.
type NamingContract struct {
	ProjectName  string
	Binary       string
	NameTemplate string
	GOOS         []string
	GOARCH       []string
	Format       string
	// FormatOverrides maps a GOOS to the archive format it uses instead
	// of Format. Windows ships a zip because that is what a Windows user
	// can open without installing anything.
	FormatOverrides map[string]string
	ChecksumName    string
}

// goreleaserConfig models only the keys the naming contract depends on.
//
// Deliberately not KnownFields(true): the file legitimately carries
// builds.env, builds.ldflags, release.header, changelog.use and more,
// none of which touch a filename, and failing on them would make every
// unrelated release-config edit a broken release. The fail-closed
// guarantee comes instead from checkNamingShape below, which refuses
// every *shape* that would move the naming contract somewhere this tool
// is not looking.
type goreleaserConfig struct {
	ProjectName string `yaml:"project_name"`
	Builds      []struct {
		Binary  string   `yaml:"binary"`
		GOOS    []string `yaml:"goos"`
		GOARCH  []string `yaml:"goarch"`
		Targets []string `yaml:"targets"`
		Ignore  []struct {
			GOOS   string `yaml:"goos"`
			GOARCH string `yaml:"goarch"`
		} `yaml:"ignore"`
	} `yaml:"builds"`
	Archives []struct {
		Formats         []string `yaml:"formats"`
		Format          string   `yaml:"format"`
		NameTemplate    string   `yaml:"name_template"`
		FormatOverrides []struct {
			GOOS    string   `yaml:"goos"`
			Formats []string `yaml:"formats"`
			Format  string   `yaml:"format"`
		} `yaml:"format_overrides"`
	} `yaml:"archives"`
	Checksum struct {
		NameTemplate string `yaml:"name_template"`
		Disable      bool   `yaml:"disable"`
	} `yaml:"checksum"`
}

// ParseNamingContract reads .goreleaser.yaml and refuses anything it
// cannot name with confidence.
func ParseNamingContract(goreleaserYAML []byte) (NamingContract, error) {
	var cfg goreleaserConfig
	if err := yaml.Unmarshal(goreleaserYAML, &cfg); err != nil {
		return NamingContract{}, fmt.Errorf(".goreleaser.yaml is not valid YAML: %w", err)
	}
	if err := checkNamingShape(cfg); err != nil {
		return NamingContract{}, err
	}
	build, archive := cfg.Builds[0], cfg.Archives[0]

	overrides := make(map[string]string, len(archive.FormatOverrides))
	for _, o := range archive.FormatOverrides {
		overrides[o.GOOS] = o.Formats[0]
	}
	return NamingContract{
		ProjectName:     cfg.ProjectName,
		Binary:          build.Binary,
		NameTemplate:    archive.NameTemplate,
		GOOS:            build.GOOS,
		GOARCH:          build.GOARCH,
		Format:          archive.Formats[0],
		FormatOverrides: overrides,
		ChecksumName:    cfg.Checksum.NameTemplate,
	}, nil
}

// checkNamingShape is the fail-closed half. Each rule here names a way
// the artifact names could move without this tool noticing; the answer
// to every one of them is to stop, not to guess.
func checkNamingShape(cfg goreleaserConfig) error {
	if cfg.ProjectName == "" {
		return fmt.Errorf(".goreleaser.yaml has no project_name")
	}
	if len(cfg.Builds) != 1 {
		return fmt.Errorf(".goreleaser.yaml has %d builds entries, and this check models exactly 1: "+
			"more than one build means more than one artifact matrix", len(cfg.Builds))
	}
	if len(cfg.Archives) != 1 {
		return fmt.Errorf(".goreleaser.yaml has %d archives entries, and this check models exactly 1", len(cfg.Archives))
	}
	build, archive := cfg.Builds[0], cfg.Archives[0]
	if len(build.Targets) != 0 {
		return fmt.Errorf(".goreleaser.yaml sets builds[0].targets, which replaces the goos × goarch matrix this check expands")
	}
	if len(build.Ignore) != 0 {
		return fmt.Errorf(".goreleaser.yaml sets builds[0].ignore, which removes pairs from the matrix this check expands")
	}
	if len(build.GOOS) == 0 || len(build.GOARCH) == 0 {
		return fmt.Errorf(".goreleaser.yaml does not list both goos and goarch explicitly; this check will not assume goreleaser's defaults")
	}
	if archive.NameTemplate == "" {
		return fmt.Errorf(".goreleaser.yaml has no archives[0].name_template; this check will not assume goreleaser's default")
	}
	if len(archive.Formats) != 1 {
		return fmt.Errorf(".goreleaser.yaml archives[0].formats has %d entries, want exactly 1 "+
			"(the deprecated singular `format:` key is not modeled; use `formats:`)", len(archive.Formats))
	}
	for _, o := range archive.FormatOverrides {
		if o.GOOS == "" {
			return fmt.Errorf(".goreleaser.yaml has a format_overrides entry with no goos")
		}
		if len(o.Formats) != 1 {
			return fmt.Errorf(".goreleaser.yaml format_overrides for %q has %d formats, want exactly 1", o.GOOS, len(o.Formats))
		}
	}
	if cfg.Checksum.Disable {
		return fmt.Errorf(".goreleaser.yaml disables the checksum file, which npm/install.js requires to verify a download")
	}
	if cfg.Checksum.NameTemplate == "" {
		return fmt.Errorf(".goreleaser.yaml has no checksum.name_template; this check will not assume goreleaser's default")
	}
	return nil
}

// archiveExtensions is every archive format whose file extension this
// tool is willing to state. A format outside it — `binary`, tar.xz,
// tar.zst — changes what a release asset is called and, for `binary`,
// what it even is, so it stops here instead of being guessed at.
var archiveExtensions = map[string]string{
	"tar.gz": ".tar.gz",
	"tgz":    ".tgz",
	"zip":    ".zip",
}

// templateFields is what a name_template may refer to.
//
// The set is closed on purpose. text/template errors on a field a struct
// does not have, so a template reaching for .Arm, .Mips or .Amd64 — any
// of which goreleaser would resolve to something this tool has no way to
// predict — fails here rather than producing a plausible wrong name.
type templateFields struct {
	ProjectName string
	Binary      string
	Version     string
	Tag         string
	Os          string
	Arch        string
}

// ExpectedAssets is every file a release for v must carry, sorted.
func (c NamingContract) ExpectedAssets(v ReleaseVersion) ([]string, error) {
	tmpl, err := template.New("name").Parse(c.NameTemplate)
	if err != nil {
		return nil, fmt.Errorf("archives[0].name_template does not parse: %w", err)
	}
	var names []string
	for _, goos := range c.GOOS {
		format := c.Format
		if override, ok := c.FormatOverrides[goos]; ok {
			format = override
		}
		ext, ok := archiveExtensions[format]
		if !ok {
			return nil, fmt.Errorf("archive format %q for %s has no extension this check will state; "+
				"add it to archiveExtensions once you have confirmed what goreleaser names such a file", format, goos)
		}
		for _, goarch := range c.GOARCH {
			var b strings.Builder
			err := tmpl.Execute(&b, templateFields{
				ProjectName: c.ProjectName,
				Binary:      c.Binary,
				Version:     v.String(),
				Tag:         v.Tag(),
				Os:          goos,
				Arch:        goarch,
			})
			if err != nil {
				return nil, fmt.Errorf("archives[0].name_template refers to something this check cannot resolve: %w", err)
			}
			names = append(names, b.String()+ext)
		}
	}

	sums, err := template.New("checksum").Parse(c.ChecksumName)
	if err != nil {
		return nil, fmt.Errorf("checksum.name_template does not parse: %w", err)
	}
	var b strings.Builder
	if err := sums.Execute(&b, templateFields{
		ProjectName: c.ProjectName,
		Binary:      c.Binary,
		Version:     v.String(),
		Tag:         v.Tag(),
	}); err != nil {
		return nil, fmt.Errorf("checksum.name_template refers to something this check cannot resolve: %w", err)
	}
	names = append(names, b.String())

	sort.Strings(names)
	return names, nil
}

// AssetDiff is the comparison between what a release should carry and
// what it does.
type AssetDiff struct {
	Want       []string
	Have       []string
	Missing    []string
	Unexpected []string
}

// Complete reports whether the release carries exactly the expected set.
//
// Exactly, in both directions. Missing is the obvious failure — the npm
// wrapper 404s on the platform whose archive never uploaded. Unexpected
// is the less obvious one and is treated the same way: an asset nobody
// predicted means the release config grew an artifact kind this check
// does not understand, and a release verifier that shrugs at files it
// cannot name is not verifying the release.
func (d AssetDiff) Complete() bool {
	return len(d.Missing) == 0 && len(d.Unexpected) == 0
}

// DiffAssets compares an expected asset set against the names actually
// attached to the release.
func DiffAssets(want, have []string) AssetDiff {
	haveSet := make(map[string]bool, len(have))
	for _, h := range have {
		haveSet[h] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	d := AssetDiff{Want: append([]string(nil), want...), Have: append([]string(nil), have...)}
	for _, w := range want {
		if !haveSet[w] {
			d.Missing = append(d.Missing, w)
		}
	}
	for _, h := range have {
		if !wantSet[h] {
			d.Unexpected = append(d.Unexpected, h)
		}
	}
	sort.Strings(d.Want)
	sort.Strings(d.Have)
	sort.Strings(d.Missing)
	sort.Strings(d.Unexpected)
	return d
}

// Error is the diff as a caller should print it: both directions named,
// because "the release is wrong" without saying how is a message that
// sends a human to the GitHub UI to work it out.
func (d AssetDiff) Error() error {
	if d.Complete() {
		return nil
	}
	var parts []string
	if len(d.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("missing %d asset(s): %s", len(d.Missing), strings.Join(d.Missing, ", ")))
	}
	if len(d.Unexpected) > 0 {
		parts = append(parts, fmt.Sprintf("unexpected %d asset(s): %s", len(d.Unexpected), strings.Join(d.Unexpected, ", ")))
	}
	return fmt.Errorf("the GitHub Release does not carry the expected assets: %s", strings.Join(parts, "; "))
}
