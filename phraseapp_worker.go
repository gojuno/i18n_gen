package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/phrase/phraseapp-go/phraseapp"
)

type (
	PhraseappWorker interface {
		// Upload uploads to phraseapp specified locale. Locale is a go-i18n json.
		Upload(ctx PhraseappContexter)
		// Download downloads from phraseapp all projects and all their locales. Locale is a go-i18n json.
		Download(ctx PhraseappContexter)
	}

	PhraseappContexter interface {
		Projects() map[string]string
		ErrorHandler(error)
		Etag(project, lang string) string
		OnDownload(project, lang, newEtag string, data []byte)
		OnUpload(project, lang string)
		GetLocalesForUpdate() map[string][]string
		UpdateTranslationFlag() bool
	}

	PhraseappWorkerContext struct {
		Client *phraseapp.Client
		Cfg    *phraseapp.Config
	}
)

func NewPhraseappWorker(cfg *phraseapp.Config, client *phraseapp.Client) *PhraseappWorkerContext {
	return &PhraseappWorkerContext{
		Client: client,
		Cfg:    cfg,
	}
}

// Upload invokes PhraseappContexter.OnUpload on successful upload.
func (c *PhraseappWorkerContext) Upload(ctx PhraseappContexter) {
	locales := ctx.GetLocalesForUpdate()
	for k, bufs := range locales {
		strs := strings.Split(k, ":")
		project, lang := strs[0], strs[1]
		projectId, ok := ctx.Projects()[project]
		if !ok {
			ctx.ErrorHandler(fmt.Errorf("Config is broken, phraseapp project id for %s is not specified", project))
			continue
		}
		for _, buf := range bufs {
			c.uploadLocaleImpl(ctx, projectId, project, lang, []byte(buf))
		}
	}
}

// Download invokes PhraseappContexter.OnDownload on successful download.
func (c *PhraseappWorkerContext) Download(ctx PhraseappContexter) {
	for name, projectId := range ctx.Projects() {
		locales, err := c.getLocales(ctx, projectId)
		if err != nil {
			ctx.ErrorHandler(err)
			continue
		}
		for _, locale := range locales {
			err = c.downloadLocale(ctx, projectId, name, locale.ID, locale.Name)
			if err != nil {
				ctx.ErrorHandler(err)
			}
		}
	}
}

func (c *PhraseappWorkerContext) getLocales(ctx PhraseappContexter, projectId string) ([]*phraseapp.Locale, error) {
	allLocales := []*phraseapp.Locale{}
	for i := 0; ; i++ {
		locales, err := c.Client.LocalesList(projectId, i, *c.Cfg.PerPage)
		if err != nil {
			return nil, fmt.Errorf("Unable to get locale list for project %s, %v", projectId, err)
		}
		allLocales = append(allLocales, locales...)
		if len(locales) < *c.Cfg.PerPage {
			break
		}
	}
	return allLocales, nil
}

func (c *PhraseappWorkerContext) downloadLocale(ctx PhraseappContexter, projectId, project, langId, lang string) error {
	etag := ctx.Etag(project, lang)
	data, etag, err := c.downloadLocaleImpl(ctx, projectId, project, langId, lang, etag)
	if err != nil {
		return err
	} else if len(data) == 0 {
		return nil
	}
	ctx.OnDownload(project, lang, etag, data)
	return nil
}

func (c *PhraseappWorkerContext) downloadLocaleImpl(ctx PhraseappContexter, projectId, project, langId, lang, etag string) ([]byte, string, error) {
	params := phraseapp.LocaleDownloadParams{FileFormat: &c.Cfg.DefaultFileFormat}

	url := fmt.Sprintf("/v2/projects/%s/locales/%s/download", projectId, langId)
	paramsBuf := bytes.NewBuffer(nil)
	err := json.NewEncoder(paramsBuf).Encode(&params)
	if err != nil {
		return nil, "", fmt.Errorf("Unable to encode url %s, %v, %s, %s", url, err, project, lang)
	}
	endpointUrl := c.Client.Credentials.Host + url
	req, err := http.NewRequest("GET", endpointUrl, paramsBuf)
	if err != nil {
		return nil, "", fmt.Errorf("Unable to create request %s, %v, %s, %s", endpointUrl, err, project, lang)
	}
	req.Header.Add("Content-Type", "application/json")
	req.Header.Set("User-Agent", phraseapp.GetUserAgent())
	req.Header.Set("Authorization", "token "+c.Client.Credentials.Token)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	localClient := http.Client{}
	resp, err := localClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("Unable to do http request %s, %v, %s, %s", endpointUrl, err, project, lang)
	}
	defer resp.Body.Close()
	newEtag, ok := resp.Header["Etag"]
	if !ok {
		return nil, "", fmt.Errorf("Expected etag argument %s, %v, %s, %s", endpointUrl, project, lang, resp.Header)
	}
	if resp.StatusCode == 304 {
		return nil, "", nil
	}
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("Error on http request  %s, %v, %s, %s", resp.Status, endpointUrl, project, lang)
	}
	retVal, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("Unable to read body %#v, %s, %s", resp.Body, project, lang)
	}

	return retVal, newEtag[0], nil
}

func (c *PhraseappWorkerContext) uploadLocaleImpl(ctx PhraseappContexter, projectId, project, lang string, buf []byte) {
	url := fmt.Sprintf("/v2/projects/%s/uploads", projectId)
	paramsBuf := bytes.NewBuffer(nil)
	writer := multipart.NewWriter(paramsBuf)
	ctype := writer.FormDataContentType()

	part, err := writer.CreateFormFile("file", lang+".json")
	if err != nil {
		ctx.ErrorHandler(err)
		return
	}
	_, err = part.Write(buf)
	if err != nil {
		ctx.ErrorHandler(err)
		return
	}
	err = writer.WriteField("locale_id", lang)
	if err != nil {
		ctx.ErrorHandler(err)
		return
	}
	err = writer.WriteField("update_translations", strconv.FormatBool(ctx.UpdateTranslationFlag()))
	if err != nil {
		ctx.ErrorHandler(err)
		return
	}
	err = writer.WriteField("file_format", c.Cfg.DefaultFileFormat)
	if err != nil {
		ctx.ErrorHandler(err)
		return
	}
	// Code was taken from original library "github.com/phrase/phraseapp-go/phraseapp/lib.go"
	err = writer.WriteField("utf8", "✓")
	if err != nil {
		ctx.ErrorHandler(err)
		return
	}
	writer.Close()

	endpointUrl := c.Client.Credentials.Host + url
	req, err := http.NewRequest("POST", endpointUrl, paramsBuf)
	if err != nil {
		ctx.ErrorHandler(fmt.Errorf("Unable to create request %s, %v, %s, %s", endpointUrl, err, project, lang))
	}
	req.Header.Add("Content-Type", ctype)
	req.Header.Set("User-Agent", phraseapp.GetUserAgent())
	req.Header.Set("Authorization", "token "+c.Client.Credentials.Token)

	localClient := http.Client{}
	resp, err := localClient.Do(req)
	if err != nil {
		ctx.ErrorHandler(fmt.Errorf("Unable to do http request %s, %v, %s, %s", endpointUrl, err, project, lang))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		ctx.ErrorHandler(fmt.Errorf("Error on http request  %s, %v, %s, %s", resp.Status, endpointUrl, project, lang))
		return
	}
	ctx.OnUpload(project, lang)
}
