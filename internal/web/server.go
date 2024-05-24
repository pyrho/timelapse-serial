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
	"time"

	"log"
	"net/http"

    "github.com/nozzle/throttler"
	"github.com/pyrho/timelapse-serial/internal/config"
	"github.com/pyrho/timelapse-serial/internal/web/assets"
)

func getSnapshotsThumbnails(folderName string, outputDir string, maxRoutines int) []Hi {
    log.Println("Creating all thumbnails.")
	mu := sync.Mutex{}
	var allThumbs []Hi
	snaps := getSnapsForTimelapseFolder(outputDir, folderName)
    t := throttler.New(maxRoutines, len(snaps))
	// var wg sync.WaitGroup
	for ix, snap := range snaps {
		go func(sn SnapInfo, index int) {
            log.Println("Creating thumbnail...")
			imgPath := filepath.Join(outputDir, sn.FolderName, sn.FileName)
			thumbPath := CreateAndSaveThumbnail(imgPath)
			thumbRelativePath, err := filepath.Rel(outputDir, thumbPath)
			if err != nil {
				log.Println(err)
				thumbRelativePath = ""
			}
			t.Done(err)
			mu.Lock()
			allThumbs = append(allThumbs, Hi{
				ThumbnailPath: thumbRelativePath,
				ix:            index,
				ImgPath:       sn.FolderName + "/" + sn.FileName,
			})
			mu.Unlock()
		}(snap, ix)

        errorCount := t.Throttle()
        if errorCount > 0 {
            log.Println("image/resize: errorCount > 0")
        }
	}
	// wg.Wait()
	slices.SortFunc(allThumbs, func(a, b Hi) int {
		return b.ix - a.ix
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
			"AllThumbs":    getSnapshotsThumbnails(folderName, conf.Camera.OutputDir, conf.Web.ThumbnailCreationMaxGoroutines),
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
			allThumbs = getSnapshotsThumbnails(firstTimelapseFolderName, conf.Camera.OutputDir, conf.Web.ThumbnailCreationMaxGoroutines)
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
	slices.SortFunc(tl, func(a, b TLInfo) int {
		aTime, _ := folderNameToTime(a.FolderName)
		bTime, _ := folderNameToTime(b.FolderName)

		if bTime.Before(aTime) {
			return -1
		} else if bTime.After(aTime){
			return 1
		} else {
            return 0
        }
	})
	return tl
}

func folderNameToTime(folderName string) (time.Time, error) {
    layout := "2006-01-02-15-04-05"
	date, err := time.Parse(layout, folderName)
	if err != nil {
        log.Println(err)
		return time.Now(), err
	} else {
		return date, nil

	}
}
