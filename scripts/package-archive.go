//go:build ignore

package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var epoch = time.Unix(0, 0).UTC()

func main() {
	source := flag.String("source", "", "directory to archive")
	output := flag.String("output", "", "output .tar.gz or .zip")
	flag.Parse()
	if *source == "" || *output == "" {
		log.Fatal("source and output are required")
	}
	entries, err := archiveEntries(*source)
	if err != nil {
		log.Fatal(err)
	}
	switch {
	case strings.HasSuffix(*output, ".tar.gz"):
		err = writeTarGzip(*source, *output, entries)
	case strings.HasSuffix(*output, ".zip"):
		err = writeZip(*source, *output, entries)
	default:
		log.Fatal("output must end in .tar.gz or .zip")
	}
	if err != nil {
		log.Fatal(err)
	}
}

func archiveEntries(source string) ([]string, error) {
	parent := filepath.Dir(filepath.Clean(source))
	var entries []string
	err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return &os.PathError{Op: "archive unsupported file", Path: path, Err: os.ErrInvalid}
		}
		relative, err := filepath.Rel(parent, path)
		if err != nil {
			return err
		}
		entries = append(entries, relative)
		return nil
	})
	sort.Strings(entries)
	return entries, err
}

func writeTarGzip(source, output string, entries []string) error {
	file, err := os.OpenFile(output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.Header.ModTime = epoch
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	err = writeEntries(source, entries, func(path, name string, info os.FileInfo) error {
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(name)
		header.Mode = int64(normalizedMode(info).Perm())
		if info.IsDir() {
			header.Name += "/"
		}
		header.ModTime, header.AccessTime, header.ChangeTime = epoch, time.Time{}, time.Time{}
		header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
		header.Format = tar.FormatUSTAR
		if err := tarWriter.WriteHeader(header); err != nil || info.IsDir() {
			return err
		}
		return copyFile(tarWriter, path)
	})
	return closeAll(err, tarWriter.Close, gzipWriter.Close, file.Close)
}

func writeZip(source, output string, entries []string) error {
	file, err := os.OpenFile(output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	zipWriter := zip.NewWriter(file)
	err = writeEntries(source, entries, func(path, name string, info os.FileInfo) error {
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(name)
		header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		header.SetMode(normalizedMode(info))
		if info.IsDir() {
			header.Name += "/"
		} else {
			header.Method = zip.Deflate
		}
		writer, err := zipWriter.CreateHeader(header)
		if err != nil || info.IsDir() {
			return err
		}
		return copyFile(writer, path)
	})
	return closeAll(err, zipWriter.Close, file.Close)
}

func normalizedMode(info os.FileInfo) os.FileMode {
	if info.IsDir() || info.Mode().Perm()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func writeEntries(source string, entries []string, write func(string, string, os.FileInfo) error) error {
	parent := filepath.Dir(filepath.Clean(source))
	for _, name := range entries {
		path := filepath.Join(parent, name)
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if err := write(path, name, info); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(destination io.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(destination, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func closeAll(err error, closes ...func() error) error {
	for _, close := range closes {
		if closeErr := close(); err == nil {
			err = closeErr
		}
	}
	return err
}
