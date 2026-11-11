package dockgithub

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
)

// FilesBranch is the branch a dock commits the files of its replies to
// (an orphan branch, made on first use): comments link them at that
// commit, so a link keeps working after later files.
const FilesBranch = "seaport-files"

// filesTries bounds the retries of a branch update another reply raced.
const filesTries = 3

// commitFiles commits files to FilesBranch of repo under
// pr-<number>/<message id>/ in one commit and returns each file's link
// (at that commit), in order.
func (c *client) commitFiles(ctx context.Context, a App, web, repo string, number int, messageID string, files []dock.File) ([]string, error) {
	tok, err := c.repoToken(ctx, a, repo)
	if err != nil {
		return nil, err
	}
	dir := "pr-" + strconv.Itoa(number) + "/" + messageID + "/"
	type entry struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	entries := make([]entry, len(files))
	for i, f := range files {
		var blob struct {
			SHA string `json:"sha"`
		}
		err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/git/blobs", tok,
			map[string]string{"content": base64.StdEncoding.EncodeToString(f.Data), "encoding": "base64"}, &blob)
		if err != nil {
			return nil, errx.Wrap(err, "upload "+f.Name, errx.TypeExternal)
		}
		entries[i] = entry{Path: dir + f.Name, Mode: "100644", Type: "blob", SHA: blob.SHA}
	}
	var commit string
	for try := 1; ; try++ {
		head, tree, err := c.branchHead(ctx, tok, repo)
		if err != nil {
			return nil, err
		}
		body := map[string]any{"tree": entries}
		if tree != "" {
			body["base_tree"] = tree
		}
		var t struct {
			SHA string `json:"sha"`
		}
		if err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/git/trees", tok, body, &t); err != nil {
			return nil, errx.Wrap(err, "files tree", errx.TypeExternal)
		}
		parents := []string{}
		if head != "" {
			parents = append(parents, head)
		}
		var cm struct {
			SHA string `json:"sha"`
		}
		err = c.do(ctx, http.MethodPost, "/repos/"+repo+"/git/commits", tok, map[string]any{
			"message": "Seaport files for " + repo + "#" + strconv.Itoa(number), "tree": t.SHA, "parents": parents}, &cm)
		if err != nil {
			return nil, errx.Wrap(err, "files commit", errx.TypeExternal)
		}
		if head == "" {
			err = c.do(ctx, http.MethodPost, "/repos/"+repo+"/git/refs", tok,
				map[string]string{"ref": "refs/heads/" + FilesBranch, "sha": cm.SHA}, nil)
		} else {
			err = c.do(ctx, http.MethodPatch, "/repos/"+repo+"/git/refs/heads/"+FilesBranch, tok,
				map[string]any{"sha": cm.SHA, "force": false}, nil)
		}
		if err == nil {
			commit = cm.SHA
			break
		}
		// Another reply moved (or made) the branch meanwhile: on top again.
		if try == filesTries || errx.IsType(err, errx.TypeNotFound) {
			return nil, errx.Wrap(err, "update "+FilesBranch, errx.TypeExternal)
		}
	}
	links := make([]string, len(files))
	for i, e := range entries {
		segs := strings.Split(e.Path, "/")
		for j, s := range segs {
			segs[j] = url.PathEscape(s)
		}
		links[i] = strings.TrimRight(web, "/") + "/" + repo + "/blob/" + commit + "/" + strings.Join(segs, "/")
	}
	return links, nil
}

// branchHead is FilesBranch's commit and tree ("" when it doesn't exist).
func (c *client) branchHead(ctx context.Context, tok, repo string) (string, string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/git/ref/heads/"+FilesBranch, tok, nil, &ref)
	if errx.IsType(err, errx.TypeNotFound) {
		return "", "", nil
	}
	if err != nil {
		return "", "", errx.Wrap(err, "read "+FilesBranch, errx.TypeExternal)
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/git/commits/"+ref.Object.SHA, tok, nil, &commit); err != nil {
		return "", "", errx.Wrap(err, "read "+FilesBranch, errx.TypeExternal)
	}
	return ref.Object.SHA, commit.Tree.SHA, nil
}

// filesBody is the comment's text plus the files: images shown, the rest
// linked.
func filesBody(text string, files []dock.File, links []string) string {
	var b strings.Builder
	b.WriteString(text)
	if text != "" {
		b.WriteString("\n\n")
	}
	for i, f := range files {
		name := strings.NewReplacer("[", "", "]", "").Replace(f.Name)
		if f.IsImage() {
			b.WriteString("![" + name + "](" + links[i] + "?raw=true)\n")
			continue
		}
		b.WriteString("📎 [" + name + "](" + links[i] + ")\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
