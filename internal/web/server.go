package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"

	// For debugging
	// _ "net/http/pprof"

	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"log"
	"net/http"

	"github.com/pyrho/timelapse-serial/internal/config"
	"github.com/pyrho/timelapse-serial/internal/utils"
	"github.com/pyrho/timelapse-serial/internal/web/assets"
	"github.com/pyrho/timelapse-serial/internal/web/vendor"
)

const FOLDERS_PER_PAGE = 5

func getTimelapseFolderSubSlice(allFolders []TLInfo, n int) []TLInfo {
	// 0 => 0..4
	// 1 => 5..9
	// 2 => 10..14
	// n => (n*5)..(n+4)
	var tmp []TLInfo
	if len(allFolders) <= FOLDERS_PER_PAGE {
		tmp = allFolders
	} else if len(allFolders) < n*FOLDERS_PER_PAGE+FOLDERS_PER_PAGE {
		tmp = allFolders[n*FOLDERS_PER_PAGE:]
	} else {
		tmp = allFolders[n*FOLDERS_PER_PAGE : (n*FOLDERS_PER_PAGE)+FOLDERS_PER_PAGE]
	}

	return tmp

}

func getSnapshotsThumbnails(folderName string, outputDir string, maxRoutines int, ctx context.Context) []Hi {
	log.Println("Creating all thumbnails")
	mu := sync.Mutex{}
	var allThumbs []Hi
	snaps := getSnapsForTimelapseFolder(outputDir, folderName)
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxRoutines)
	nbSnaps := len(snaps)
	for ix, snap := range snaps {
		wg.Add(1)
		sem <- struct{}{} // Acquire semaphore
		go func(sn SnapInfo, index int) {
			defer wg.Done()
			imgPath := filepath.Join(outputDir, sn.FolderName, sn.FileName)
			thumbPath := CreateAndSaveThumbnail(imgPath, ctx)
			thumbRelativePath, _ := filepath.Rel(outputDir, thumbPath)

			mu.Lock()
			allThumbs = append(allThumbs, Hi{
				ThumbnailPath: thumbRelativePath,
				ix:            index,
				ImgPath:       sn.FolderName + "/" + sn.FileName,
			})
			mu.Unlock()

			log.Printf("Thumbnail [%d/%d] created and added to slice", index, nbSnaps)
			<-sem
		}(snap, ix)

	}

	// Wait for all goroutines to finish
	done := make(chan struct{})
	go func() {
		wg.Wait()
		log.Println("Done waiting!")
		close(done)
	}()

	select {
	case <-done:
		log.Println("All thumbnails created!")
		slices.SortFunc(allThumbs, func(a, b Hi) int {
			return b.ix - a.ix
		})
		return allThumbs
	case <-ctx.Done():
		log.Println("Thumbnail creation aborted")
		return []Hi{}
	}

}

func StartWebServer(conf *config.Config) {

	printerInfoEnabled := len(conf.Web.PrinterUrl) > 0
    log.Println(printerInfoEnabled )
	var printerInfoCache *printInfoCache

	if printerInfoEnabled {
		printerInfoCache = newPrintInfoCache()
		printerInfoCache.startLoop(conf.Web.PrinterUrl, conf.Web.PrusaLinkKey)
	}

	http.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, assets.All, "favicon.ico")
	})
	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServerFS(assets.All)))
	http.Handle("/vendor/", http.StripPrefix("/vendor/", http.FileServerFS(vendor.All)))

	http.Handle("/serve/", http.StripPrefix("/serve/", http.FileServer(http.Dir(conf.Camera.OutputDir))))

	http.HandleFunc("/get-printer-status5", func(w http.ResponseWriter, r *http.Request) {
        if !printerInfoEnabled {
            return
        }

		state := printerInfoCache.get()
		template := template.Must(template.ParseFS(Templates, "templates/title.html"))

		days, hours, minutes := utils.SecondsToHumanDuration(state.Job.TimeRemaining)

		if err := template.ExecuteTemplate(w, "title", map[string]interface{}{
			"State":    state.Printer.State,
			"Progress": state.Job.Progress,
			"Remaining": map[string]interface{}{
				"Days":    days,
				"Hours":   hours,
				"Minutes": minutes,
			},
			"Refresh":           "every 5s",
			"WithPrinterStatus": true,
		}); err != nil {
			log.Printf("Cannot execute template title, %s\n", err)
		}
	})

	http.HandleFunc("/clicked/{folderName}", func(w http.ResponseWriter, r *http.Request) {
		folderName := r.PathValue("folderName")
		timelapseVideoPath := fmt.Sprintf("%s/%s/output.mp4", conf.Camera.OutputDir, folderName)
		hasTimelapseVideo := true
		if _, err := os.Stat(timelapseVideoPath); errors.Is(err, os.ErrNotExist) {
			hasTimelapseVideo = false
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		template := template.Must(template.ParseFS(Templates, "templates/snaps.html"))
		if err := template.ExecuteTemplate(w, "snaps", map[string]interface{}{
			"AllThumbs":    getSnapshotsThumbnails(folderName, conf.Camera.OutputDir, conf.Web.ThumbnailCreationMaxGoroutines, ctx),
			"FolderName":   folderName,
			"HasTimelapse": hasTimelapseVideo,
		}); err != nil {
			log.Printf("Cannot execute template snaps, %s\n", err)
		}
	})

	http.HandleFunc("/modal/{folder}/{file}", func(w http.ResponseWriter, r *http.Request) {
		template := template.Must(template.ParseFS(Templates, "templates/modal.html"))
		if err := template.ExecuteTemplate(w, "modal", map[string]interface{}{
			"ImgPath": r.PathValue("folder") + "/" + r.PathValue("file"),
		}); err != nil {
			log.Printf("Cannot execute template modal, %s\n", err)
		}
	})

	http.HandleFunc("/get-folder-page/{num}", func(w http.ResponseWriter, r *http.Request) {
		folderPageNumber := r.PathValue("num")
		n, _ := strconv.Atoi(folderPageNumber)
		timelapseFolders := getTimelapseFolders(conf.Camera.OutputDir)
		tmpl := template.Must(template.ParseFS(Templates, "templates/layout.html", "templates/folders.html"))
		subSlice := getTimelapseFolderSubSlice(timelapseFolders, n)

		if err := tmpl.ExecuteTemplate(w, "folders", map[string]interface{}{
			"Timelapses": subSlice,
		}); err != nil {
			log.Printf("Cannot execute template folders, %s\n", err)
		}
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		timelapseFolders := getTimelapseFolders(conf.Camera.OutputDir)
		var firstTimelapseFolderName string
		if len(timelapseFolders) > 0 {
			firstTimelapseFolderName = timelapseFolders[0].FolderName
		}
		timelapseVideoPath := fmt.Sprintf("%s/%s/output.mp4", conf.Camera.OutputDir, firstTimelapseFolderName)
		hasTimelapseVideo := true
		if _, err := os.Stat(timelapseVideoPath); errors.Is(err, os.ErrNotExist) {
			hasTimelapseVideo = false
		}
		templateData := map[string]interface{}{
			"Timelapses":   getTimelapseFolderSubSlice(timelapseFolders, 0),
			"HasTimelapse": hasTimelapseVideo,
			"FolderName":   firstTimelapseFolderName,
			"LiveFeedURL":  conf.Camera.LiveFeedURL,
			"Pages":        make([]int, (len(timelapseFolders)/5)+1),
		}

		if printerInfoEnabled {
			state := printerInfoCache.get()
			days, hours, minutes := utils.SecondsToHumanDuration(state.Job.TimeRemaining)
			templateData["PrinterInfo"] = map[string]interface{}{
				"WithPrinterStatus": len(conf.Web.PrinterUrl) > 0,
				"State":             state.Printer.State,
				"Progress":          state.Job.Progress,
				"Refresh":           "every 5s",
				"Remaining": map[string]interface{}{
					"Days":    days,
					"Hours":   hours,
					"Minutes": minutes,
				},
			}
		}

		template := template.Must(
			template.ParseFS(
				Templates,
				"templates/layout.html", "templates/title.html", "templates/folders.html", "templates/snaps.html", "templates/folder_nav.html"),
		)
		if err := template.Execute(w, templateData); err != nil {
			log.Printf("Cannot execute templates for main page, %s\n", err)
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

func countFiles(dirPath string) uint {
	fileCount := uint(0)

	entries, _ := os.ReadDir(dirPath)

	validSnap := regexp.MustCompile(`^snap[0-9]+.jpg$`)
	for _, entry := range entries {
		if !entry.IsDir() && validSnap.MatchString(entry.Name()) {
			fileCount++
		}
	}

	return fileCount
}

func hasTimelapseVideo(dirPath string) bool {
	entries, _ := os.ReadDir(dirPath)

	validSnap := regexp.MustCompile(`^output.mp4$`)
	for _, entry := range entries {
		if !entry.IsDir() && validSnap.MatchString(entry.Name()) {
			return true
		}
	}

	return false
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
				FolderPath:        filepath.Join(outputDir, file.Name()),
				FolderName:        file.Name(),
				NumberOfSnaps:     countFiles(filepath.Join(outputDir, file.Name())),
				HasTimelapseVideo: hasTimelapseVideo(filepath.Join(outputDir, file.Name())),
			})
		}
	}
	slices.SortFunc(tl, func(a, b TLInfo) int {
		aTime, _ := folderNameToTime(a.FolderName)
		bTime, _ := folderNameToTime(b.FolderName)

		if bTime.Before(aTime) {
			return -1
		} else if bTime.After(aTime) {
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
