package web

import (
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"

	"log"
	"net/http"

	"github.com/pyrho/timelapse-serial/internal/config"
	"github.com/pyrho/timelapse-serial/internal/web/assets"
)

func getSnapshotsThumbnails(folderName string, outputDir string) []Hi {
	mu := sync.Mutex{}
	var allThumbs []Hi
	snaps := getSnapsForTimelapseFolder(outputDir, folderName)
	var wg sync.WaitGroup
	for ix, snap := range snaps {
		wg.Add(1)
		go func(sn SnapInfo, index int) {
			defer wg.Done()
			imgPath := filepath.Join(outputDir, sn.FolderName, sn.FileName)
			thumbPath := CreateAndSaveThumbnail(imgPath)
			thumbRelativePath, err := filepath.Rel(outputDir, thumbPath)
			if err != nil {
				log.Println(err)
				thumbRelativePath = ""
			}
			mu.Lock()
			allThumbs = append(allThumbs, Hi{
				ThumbnailPath: thumbRelativePath,
				ix:            index,
				ImgPath:       sn.FolderName + "/" + sn.FileName,
			})
			mu.Unlock()
		}(snap, ix)

	}
	wg.Wait()
	slices.SortFunc(allThumbs, func(a, b Hi) int {
		return a.ix - b.ix
	})
	return allThumbs

}

func StartWebServer(conf *config.Config) {
	http.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, assets.All, "favicon.ico")
	})
	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServerFS(assets.All)))

	http.Handle("/serve/", http.StripPrefix("/serve/", http.FileServer(http.Dir(conf.Camera.OutputDir))))

	http.HandleFunc("/clicked/{folderName}", func(w http.ResponseWriter, r *http.Request) {
		folderName := r.PathValue("folderName")
		timelapseVideoPath := fmt.Sprintf("%s/%s/output.mp4", conf.Camera.OutputDir, folderName)
		hasTimelapseVideo := true
		if _, err := os.Stat(timelapseVideoPath); errors.Is(err, os.ErrNotExist) {
			hasTimelapseVideo = false
		}
		template := template.Must(template.ParseFS(Templates, "templates/snaps.html"))
		if err := template.ExecuteTemplate(w, "snaps", map[string]interface{}{
			"AllThumbs":    getSnapshotsThumbnails(folderName, conf.Camera.OutputDir),
			"FolderName":   folderName,
			"HasTimelapse": hasTimelapseVideo,
		}); err != nil {
			log.Fatalf("Cannot execute template snaps, %s\n", err)
		}
	})

	http.HandleFunc("/modal/{folder}/{file}", func(w http.ResponseWriter, r *http.Request) {
		template := template.Must(template.ParseFS(Templates, "templates/modal.html"))
		if err := template.ExecuteTemplate(w, "modal", map[string]interface{}{
			"ImgPath": r.PathValue("folder") + "/" + r.PathValue("file"),
		}); err != nil {
			log.Fatalf("Cannot execute template snaps, %s\n", err)
		}
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {

		timelapseFolders := getTimelapseFolders(conf.Camera.OutputDir)
		var firstTimelapseFolderName string
		var allThumbs []Hi
		if len(timelapseFolders) > 0 {
			firstTimelapseFolderName = timelapseFolders[0].FolderName
			allThumbs = getSnapshotsThumbnails(firstTimelapseFolderName, conf.Camera.OutputDir)
		}
		timelapseVideoPath := fmt.Sprintf("%s/%s/output.mp4", conf.Camera.OutputDir, firstTimelapseFolderName)
		hasTimelapseVideo := true
		if _, err := os.Stat(timelapseVideoPath); errors.Is(err, os.ErrNotExist) {
			hasTimelapseVideo = false
		}
		templateData := map[string]interface{}{
			"Timelapses":   timelapseFolders,
			"AllThumbs":    allThumbs,
			"HasTimelapse": hasTimelapseVideo,
			"FolderName":   firstTimelapseFolderName,
			"LiveFeedURL":  conf.Camera.LiveFeedURL,
		}

		template := template.Must(template.ParseFS(Templates, "templates/layout.html", "templates/folders.html", "templates/snaps.html"))
		if err := template.Execute(w, templateData); err != nil {
			log.Fatal(err)
		}
	})
	log.Println("HTTP server running")
	log.Fatal(http.ListenAndServe(":3025", nil))

}

func getSnapsForTimelapseFolder(outputDir string, folderName string) []SnapInfo {
	validSnap := regexp.MustCompile(`^snap[0-9]+.jpg$`)
	var tl []SnapInfo
	files, err := os.ReadDir(filepath.Join(outputDir, folderName))
	if err != nil {
		log.Fatalf("1: Cannot read output dir: %s", err)
	}
	for _, file := range files {
		if !file.IsDir() && validSnap.MatchString(file.Name()) {
			tl = append(tl, SnapInfo{
				FilePath:   filepath.Join(outputDir, file.Name()),
				FolderName: folderName,
				FileName:   file.Name(),
			})
		}
	}
	return tl
}
func getTimelapseFolders(outputDir string) []TLInfo {
	validDir := regexp.MustCompile(`^[0-9-]+$`)
	var tl []TLInfo
	// var tl2 map[string][]string
	files, err := os.ReadDir(outputDir)
	if err != nil {
		log.Fatalf("2: Cannot read output dir: %s", err)
	}
	for _, file := range files {
		if file.IsDir() && validDir.MatchString(file.Name()) {
			tl = append(tl, TLInfo{
				FolderPath: filepath.Join(outputDir, file.Name()),
				FolderName: file.Name(),
			})
		}
	}
	return tl
}
