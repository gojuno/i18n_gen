package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"hash/crc32"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phrase/phraseapp-go/phraseapp"
)

const (
	INVALID_CRC32         = 0
	LOCALIZED_DATA_FOLDER = "localized_data"
	GLOBAL_RUN_DELAY      = 2e9 // nanoseconds
	BACKEND               = "Backend"
)

type (
	CheckSum struct {
		DataCrc32   uint32 `json:"crc32"`
		ETag        string `json:"etag"`
		ProjectName string `json:"project"`
		LocaleName  string `json:"locale"`
	}

	CheckSumList []*CheckSum

	RunInfo struct {
		CheckSumList CheckSumList `json:"lst"`
		LastRunTime  int64        `json:"last_run_time"`
	}

	i18nGenContext struct{}

	projectIds map[string]string
)

func (i *projectIds) String() string {
	cont := []string{}
	for k, v := range *i {
		cont = append(cont, k+":"+v)
	}
	return strings.Join(cont, " ")
}

func (i *projectIds) Set(value string) error {
	values := strings.Split(value, ":")
	phraseappProjects[values[0]] = values[1]
	return nil
}

var (
	ctx               *PhraseappWorkerContext
	runInfo           RunInfo
	basepath          string
	defaultProject    string
	defaultLocale     string
	phraseappProjects projectIds
)

func main() {
	phraseappProjects = projectIds{}
	junolabPath := flag.String("path", "junolab.net", "path to micro-services")
	phraseappToken := flag.String("token", "", "token for phraseapp")
	defaultProject = *flag.String("project", BACKEND, "default project name")
	defaultLocale = *flag.String("locale", "en-US", "default locale name")
	flag.Var(&phraseappProjects, "project_id", "pair of project name and prhaseapp id, Backend:phraseapp_project_id")

	flag.Parse()

	if *phraseappToken == "" && *junolabPath == "" {
		log.Fatalln("All params are empty.")
	}

	if *phraseappToken == "" {
		log.Fatalln("Please, specify phraseapp token")
	}

	if *junolabPath == "" {
		log.Fatalln("Please, specify path to micro-services")
		return
	}

	if _, ok := phraseappProjects[defaultProject]; !ok {
		log.Fatalln("Please, specify phraseapp project id for default project")
		return
	}

	basepath = *junolabPath

	if checkInternetConnectivity() == 0 {
		log.Fatal("There is no internet connection.")
	}

	cfg := createConfig(*phraseappToken)

	client, err := phraseapp.NewClient(cfg.Credentials)
	if err != nil {
		log.Fatalln("Unable to create client", err)
	}

	ctx = NewPhraseappWorker(cfg, client)
	readRunInfo()
	processLocales()
	writeRunInfo()
}

func createConfig(token string) *phraseapp.Config {
	cfg := new(phraseapp.Config)
	cfg.Credentials = new(phraseapp.Credentials)
	cfg.Credentials.Token = token
	cfg.DefaultFileFormat = "go_i18n"
	perPage := 25
	cfg.PerPage = &perPage
	return cfg
}

func (c *CheckSumList) GetETag(p, l string) string {
	for _, e := range *c {
		if e.LocaleName == l && e.ProjectName == p {
			return e.ETag
		}
	}
	return ""
}

func (c *CheckSumList) GetCrc32(p, l string) uint32 {
	for _, e := range *c {
		if e.LocaleName == l && e.ProjectName == p {
			return e.DataCrc32
		}
	}
	return INVALID_CRC32
}

func (c *CheckSumList) Upsert(p, l, etag string, crc32 uint32) {
	for _, e := range *c {
		if e.LocaleName == l && e.ProjectName == p {
			e.DataCrc32 = crc32
			e.ETag = etag
			return
		}
	}
	*c = append(*c, &CheckSum{crc32, etag, p, l})
}

func (c *i18nGenContext) Projects() map[string]string {
	return phraseappProjects
}

func (c *i18nGenContext) OnUpload(projectName, localeName string) {
	log.Printf("Translations for project %s for locale %s was uploaded successfully.\n", projectName, localeName)
}

func (c *i18nGenContext) UpdateTranslationFlag() bool {
	return false
}

func (c *i18nGenContext) GetLocalesForUpdate() map[string][]string {
	jsonData := GetLocalizationJsonFromSources(basepath)
	m := map[string][]string{}
	m[defaultProject+":"+defaultLocale] = append(m["en-US"], jsonData)
	return m
}

func (c *i18nGenContext) ErrorHandler(err error) {
	log.Fatal(err)
}

func (c *i18nGenContext) Etag(projectName, localeName string) string {
	origCrc32 := runInfo.CheckSumList.GetCrc32(projectName, localeName)
	existCrc32 := getFileCrc32(projectName, localeName)

	etag := ""
	if origCrc32 != INVALID_CRC32 && origCrc32 == existCrc32 {
		etag = runInfo.CheckSumList.GetETag(projectName, localeName)
	}
	return etag
}

func (c *i18nGenContext) OnDownload(projectName, localeName, newEtag string, data []byte) {
	log.Println("Downloaded locale", projectName, localeName)

	err := os.MkdirAll(filepath.Join(getLocalizationFolderName(), projectName), 0777)
	if err != nil {
		log.Fatalln("Unable to create folder for project", projectName, localeName, err)
	}

	err = ioutil.WriteFile(getLocalizationFileName(projectName, localeName), data, 0644)
	if err != nil {
		log.Fatalln("Unable to create locale file for project", projectName, localeName, err)
	}

	decodedData := []interface{}{}
	err = json.Unmarshal(data, &decodedData)
	if err != nil {
		log.Fatalln("Unable to unmarshal locale file for project", projectName, localeName, err)
	}

	for _, m := range decodedData {
		d := m.(map[string]interface{})
		if d["id"] == d["translation"] {
			log.Println("WARNING! There is untranslated string", d["id"], projectName, localeName)
		}
	}

	runInfo.CheckSumList.Upsert(projectName, localeName, newEtag, crc32.ChecksumIEEE(data))
}

func checkInternetConnectivity() int {
	conn, err := net.Dial("tcp", "google.com:80")
	defer conn.Close()
	if err != nil {
		return 0
	}
	return 1
}

func getRunInfoFileName() string {
	return filepath.Join(os.TempDir(), "i18n_gen_run_info.json")
}

func getLocalizationFolderName() string {
	return filepath.Join(basepath, LOCALIZED_DATA_FOLDER)
}

func getLocalizationFileName(projectName, localeName string) string {
	return filepath.Join(getLocalizationFolderName(), projectName, localeName+".json")
}

func readRunInfo() {
	file, e := os.Open(getRunInfoFileName())
	if e != nil {
		runInfo = RunInfo{}
		return
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	buff, err := ioutil.ReadAll(reader)
	if err != nil {
		log.Fatal("Unable to read check sum file", err)
	}
	err = json.Unmarshal(buff, &runInfo)
	if err != nil {
		defer os.Remove(getRunInfoFileName())
	}
}

func writeRunInfo() {
	file, e := os.Create(getRunInfoFileName())
	if e != nil {
		log.Println("Unable to write run info")
		return
	}
	defer file.Close()

	encoded, err := json.Marshal(&runInfo)
	if err != nil {
		log.Fatal("Unable to encode check sum file", err)
	}
	writer := bufio.NewWriter(file)
	_, err = writer.Write(encoded)
	if err != nil {
		log.Fatal(err)
	}
	writer.Flush()
}

func getFileCrc32(projectName, localeName string) uint32 {
	file, e := os.Open(getLocalizationFileName(projectName, localeName))
	if e != nil {
		return INVALID_CRC32
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	buff, err := ioutil.ReadAll(reader)
	if err != nil {
		log.Fatal(err)
	}

	return crc32.ChecksumIEEE(buff)
}

func processLocales() {
	if time.Now().UnixNano()-runInfo.LastRunTime <= GLOBAL_RUN_DELAY {
		os.Exit(0)
	}

	removeContents(getLocalizationFolderName())
	localCtx := &i18nGenContext{}

	ctx.Upload(localCtx)
	ctx.Download(localCtx)

	runInfo.LastRunTime = time.Now().UnixNano()
}

func removeContents(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		err = os.RemoveAll(filepath.Join(dir, name))
		if err != nil {
			return err
		}
	}
	return nil
}
