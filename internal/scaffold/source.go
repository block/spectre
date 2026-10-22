package scaffold

import (
	"context"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	"github.com/alecthomas/errors"
	"github.com/block/scaffolder"
)

// templateName keeps a template a single directory at the root of its repository.
var templateName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// openTemplate fetches the named template from config's repository, which is a
// local directory or a git source. cleanup removes what was fetched.
func openTemplate(ctx context.Context, config Config, name string) (template Template, cleanup func(), err error) {
	if config.Repo == "" {
		return template, nil, errors.New("no template repository; pass --template-repo, or build spectre with spectre.WithTemplateRepo")
	}
	if !templateName.MatchString(name) {
		return template, nil, errors.Errorf("template name %q must match %s", name, templateName)
	}
	var files fs.FS
	cleanup = func() {}
	if info, statErr := os.Stat(config.Repo); statErr == nil && info.IsDir() {
		files = os.DirFS(filepath.Join(config.Repo, name))
	} else {
		source := config.Repo + "//" + name + "?" + url.Values{"ref": {config.Ref}, "depth": {"1"}}.Encode()
		if files, cleanup, err = scaffolder.Open(ctx, source); err != nil {
			return template, nil, errors.Wrapf(err, "fetch template %q", name)
		}
	}
	manifest, err := ParseManifest(files)
	if errors.Is(err, fs.ErrNotExist) {
		cleanup()
		return template, nil, errors.Errorf("template %q not found in %s at %s", name, config.Repo, config.Ref)
	}
	if err != nil {
		cleanup()
		return template, nil, errors.Wrapf(err, "template %q", name)
	}
	return newTemplate(name, manifest, files), cleanup, nil
}
