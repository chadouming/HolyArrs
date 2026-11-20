package main

import (
	"log"
	"path/filepath"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

// newSubtitles gives the subtitles service its models folder and the
// built-in translator's model runners. A runner is started only while a
// translation runs; the caller stops them with the program.
func newSubtitles(subs *subtitles.Service, configDir string) *subtitles.Service {
	subs.ModelsDir = filepath.Join(configDir, "models")
	subs.Runners = builtin.NewPool(func(line string) { log.Print(line) })
	return subs
}
