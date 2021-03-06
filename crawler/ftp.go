package main

import (
	"github.com/jlaffaye/ftp"
	"net/url"
	"path"
	"path/filepath"
	"sync"
	"time"
)

type Ftp struct {
	URL      *url.URL
	Running  bool
	Obsolete bool
	Conn     *ftp.ServerConn

	crawler *Crawler
	mt      sync.Mutex
}

func CreateFtp(surl string, crawler *Crawler) (ftp *Ftp, err error) {
	parsedURL, err := url.Parse(surl)
	if err != nil {
		return
	}

	ftp = &Ftp{
		URL:     parsedURL,
		crawler: crawler,
	}

	crawler.Log.Print("Added ", parsedURL.String())
	return
}

// Try to connect as long as the server is not obsolete
// This function does not return errors as high-load FTPs
// do likely need hundreds of connection retries
func (elem *Ftp) ConnectLoop() {
	for !elem.Obsolete {
		conn, err := ftp.Connect(elem.URL.Host)
		if err == nil {
			elem.Conn = conn
			break
		}

		elem.crawler.Log.Print(err)
		time.Sleep(2 * time.Second)
	}
}

// LoginLoop consciously tries to login on the ftp server.
// If the given URL specified password and/or user then those
// values will be used otherwise it will fallback to anonymous:anonymous.
// This function does not return errors as high-load FTPs
// do likely need hundreds of login retries
func (elem *Ftp) LoginLoop() {
	name := "anonymous"
	pass := "anonymous"

	// Try to parse username/password from the URL
	userInfo := elem.URL.User
	if userInfo != nil {
		name = userInfo.Username()
		if _pass, ok := userInfo.Password(); ok {
			pass = _pass
		}
	}

	for i := 1; !elem.Obsolete; i++ {
		err := elem.Conn.Login(name, pass)
		if err == nil {
			break
		}

		elem.crawler.Log.Print(err)
		time.Sleep(time.Duration(i) * time.Second)
	}
}

// Send NoOps every 15 seconds so the connection is not closing
func (elem *Ftp) NoOpLoop() {
	for !elem.Obsolete {
		time.Sleep(15 * time.Second)

		func(elem *Ftp) {
			elem.mt.Lock()
			defer elem.mt.Unlock()

			elem.Conn.NoOp()
		}(elem)
	}
}

// Recursively walk through all directories
func (elem *Ftp) StartCrawling() (err error) {
	pwd, err := elem.Conn.CurrentDir()
	if err != nil {
		return
	}

	elem.crawlDirectoryRecursive(pwd)

	return
}

func (elem *Ftp) crawlDirectoryRecursive(dir string) {
	if elem.Obsolete {
		return
	}

	list, err := func(elem *Ftp) (list []*ftp.Entry, err error) {
		elem.mt.Lock()
		defer elem.mt.Unlock()

		list, err = elem.Conn.List(dir)
		return
	}(elem)

	if err != nil {
		elem.crawler.Log.Print(err)
	}

	for _, file := range list {
		ff := path.Join(dir, file.Name)

		// go deeper!
		if file.Type == ftp.EntryTypeFolder {
			elem.crawlDirectoryRecursive(ff)
		}

		// into teh elastics
		if file.Type == ftp.EntryTypeFile {
			var fservers []FtpEntry
			fservers = append(fservers, FtpEntry{
				Url:  elem.URL.String(),
				Path: ff,
			})

			fe := FileEntry{
				Servers:  fservers,
				Filename: filepath.Base(ff),
				Size:     file.Size,
			}

			_, err := elem.crawler.elasticSearch.AddFileEntry(fe)
			if err != nil {
				elem.crawler.Log.Print(err)
			}
		}
	}
}
