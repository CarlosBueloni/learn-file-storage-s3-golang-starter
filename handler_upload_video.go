package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Video ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't Validate JWT", err)
		return
	}

	fmt.Println("uploading video for video ID", videoID, "by user", userID)

	const maxSize = 1 << 30
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse from file", err)
		return
	}
	defer file.Close()

	media_type, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse Content-Type", err)
		return
	}

	if media_type != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Wrong media type for video", err)
		return
	}

	f, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating file", err)
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()

	_, err = io.Copy(f, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error saving file", err)
		return
	}

	processedPath, err := processVideoForFastStart(f.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error processing video", err)
		return
	}

	processedFile, err := os.Open(processedPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error opening processed file", err)
	}
	defer processedFile.Close()

	aspect_ratio, err := getVideoAspectRation(processedPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error getting aspect ratio", err)
	}

	prefix := func() string {
		switch aspect_ratio {
		case "16:9":
			return "landscape"
		case "9:16":
			return "portrait"
		default:
			return "other"
		}
	}()

	f.Seek(0, io.SeekStart)

	file_extension := strings.Split(media_type, "/")[1]
	key := make([]byte, 32)
	rand.Read(key)
	str := base64.URLEncoding.EncodeToString(key)
	path := prefix + "/" + str + "." + file_extension

	params := s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &path,
		Body:        processedFile,
		ContentType: &media_type,
	}

	cfg.s3Client.PutObject(r.Context(), &params)
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error getting video", err)
		return
	}
	url := "https://" + cfg.s3Bucket + ".s3." + cfg.s3Region + ".amazonaws.com/" + path
	video.VideoURL = &url

	cfg.db.UpdateVideo(video)
	respondWithJSON(w, http.StatusNoContent, nil)

}

func getVideoAspectRation(filePath string) (string, error) {
	type streams struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}

	type main struct {
		Streams []streams `json:"streams"`
	}

	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", err
	}

	var dimensions main

	if err := json.Unmarshal(stdout.Bytes(), &dimensions); err != nil {
		return "", err
	}

	if dimensions.Streams[0].Height/9 == dimensions.Streams[0].Width/16 {
		return "16:9", nil
	}

	if dimensions.Streams[0].Height/16 == dimensions.Streams[0].Width/9 {
		return "9:16", nil
	}

	return "other", nil
}

func processVideoForFastStart(filePath string) (string, error) {
	outputPath := filePath + ".processing"
	log.Printf("output: %s", outputPath)
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputPath)
	if err := cmd.Run(); err != nil {
		return "", err
	}

	return outputPath, nil
}
