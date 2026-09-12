package update

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type Info struct {
	Version      string       `json:"version"`
	Repository   string       `json:"repository"`
	OS           string       `json:"os"`
	Arch         string       `json:"arch"`
	Installation Installation `json:"installation"`
	LastResult   *ApplyResult `json:"last_result,omitempty"`
}

func Run(args []string, version, repository string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: update info|check|download|prepare|launch|apply [flags]")
	}
	fs := flag.NewFlagSet("update "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	repo := fs.String("repository", repository, "GitHub owner/repository")
	channel := fs.String("channel", "stable", "stable or prerelease")
	tag := fs.String("tag", "", "release tag")
	kind := fs.String("kind", "portable", "portable or installer")
	plan := fs.String("plan", "", "prepared upgrade plan")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected update arguments")
	}
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	cache := filepath.Join(cacheBase, "ssh-tunnel", "updates")
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	client, err := NewReleaseClient(*repo, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	var result any
	switch args[0] {
	case "info":
		info := Info{Version: version, Repository: *repo, OS: runtime.GOOS, Arch: runtime.GOARCH, Installation: inspectInstallation(executable, version, *repo, runtime.GOOS, runtime.GOARCH)}
		var last ApplyResult
		if readJSONFile(filepath.Join(cache, "last-result.json"), &last) == nil {
			info.LastResult = &last
		}
		result = info
	case "mark-portable":
		root := filepath.Dir(executable)
		if runtime.GOOS == "darwin" {
			root = filepath.Dir(filepath.Dir(root))
		}
		clientName, _, marker := layout(runtime.GOOS)
		if err := regularFile(filepath.Join(root, clientName)); err != nil {
			return errors.New("mark-portable is a build step requiring a complete client bundle")
		}
		err = writeJSONFile(filepath.Join(root, marker), manifest{1, version, *repo, runtime.GOOS, runtime.GOARCH})
		result = map[string]bool{"portable": err == nil}
	case "check":
		result, err = client.Check(ctx, version, *channel)
	case "download":
		result, err = client.Download(ctx, *tag, *kind, cache)
	case "prepare":
		var request PrepareInput
		data, readErr := io.ReadAll(io.LimitReader(input, (1<<20)+1))
		if readErr != nil || len(data) > 1<<20 {
			return errors.New("invalid or oversized upgrade request")
		}
		if err := json.Unmarshal(data, &request); err != nil {
			return err
		}
		result, err = Prepare(ctx, executable, version, *repo, cache, request)
	case "launch":
		err = Launch(*plan)
		result = map[string]bool{"ready": err == nil}
	case "apply":
		return ApplyFile(*plan)
	default:
		return errors.New("unknown update command")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}
