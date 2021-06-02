package minecraft

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/parithon/minecraftd/discord"
	"github.com/parithon/minecraftd/utils"
)

const minecraftnet = "https://www.minecraft.net/en-us/download/server/bedrock"

var (
	server      *exec.Cmd
	serverStdin io.WriteCloser
	dlregx      = regexp.MustCompile(`https://minecraft.azureedge.net/bin-linux/[^"]*`)
	verregx     = regexp.MustCompile(`bedrock-server-(.+).zip`)
	updating    = false
)

func downloadBedrockServer() (verison *string, err error) {
	log.Println("Gathering latest minecraft version")
	resp, err := http.Get(minecraftnet)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	downloadUrl := dlregx.FindString(string(body))
	version := verregx.FindStringSubmatch(downloadUrl)[1]

	log.Printf("Version: %s\n", version)

	fileUrl, err := url.Parse(downloadUrl)
	if err != nil {
		return nil, err
	}

	path := fileUrl.Path
	segments := strings.Split(path, "/")
	fileName := segments[len(segments)-1]

	file, err := os.Create(fileName)
	if err != nil {
		return nil, err
	}

	client := http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			r.URL.Opaque = r.URL.Path
			return nil
		},
	}

	log.Printf("Downloading latest Minecraft Bedrock version: %s\n", version)
	resp, err = client.Get(downloadUrl)
	if err != nil {
		os.Remove(fileName)
		return nil, err
	}

	defer resp.Body.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		os.Remove(fileName)
		return nil, err
	}

	defer file.Close()

	serverpath := fmt.Sprintf("bedrock-server-%s", version)
	log.Printf("Unzipping latest Minecraft Bedrock version: %s\n", version)
	if _, err := utils.Unzip(fileName, serverpath); err != nil {
		os.Remove(fileName)
		return nil, err
	}

	os.WriteFile(fmt.Sprintf("%s/version", serverpath), []byte(version), 0666)

	if err := os.Remove(fileName); err != nil {
		log.Println("Failed to remove zip binaries")
	}

	log.Printf("Completed downloading latest Minecraft Bedrock server version: %s", version)

	return &version, nil
}

func symlink(name string) {
	data := fmt.Sprintf("/data/%s", name)
	app := fmt.Sprintf("bedrock-server/%s", name)

	if _, err := os.Stat("/data"); os.IsNotExist(err) {
		return
	}

	if _, err := os.Stat(app); os.IsNotExist(err) {
		return
	}

	if _, err := os.Stat(data); os.IsNotExist(err) {
		if _, err := os.Stat(app); err == nil {
			if err := utils.Copy(app, data); err == nil {
				os.Remove(app)
				os.Symlink(data, app)
			}
		}
	}

}

func start(version string) {
	log.Println("Starting bedrock_server...")
	server = exec.Command("./bedrock_server")
	server.Dir = "bedrock-server"
	server.Stdout = log.Writer()

	var err error = nil
	serverStdin, err = server.StdinPipe()
	if err != nil {
		utils.Fatal("An error occurred while redirecting Stdin", err)
	}

	if err := server.Start(); err != nil {
		utils.Fatal("An error occurred while starting the bedrock_server", err)
	}

	log.Println("Started bedrock_server")

	discord.Started(version)
}

func stop() {
	for i := 6; i > 0; i-- {
		msg := fmt.Sprintf("say shutting down in %d seconds...\n", (i * 5))
		log.Println(msg)
		serverStdin.Write([]byte(msg))
		time.Sleep(time.Second * time.Duration(5))
	}
	terminate()
}

func terminate() {
	msg := "say shutting down NOW...\n"
	log.Println(msg)
	serverStdin.Write([]byte(msg))
	time.Sleep(time.Second * time.Duration(5))
	serverStdin.Write([]byte("stop\n"))
	if _, err := server.Process.Wait(); err != nil {
		utils.Fatal(err)
	}
	serverStdin.Close()
	discord.Stopped()
	log.Println("Stopped bedrock_server")
	server = nil
	serverStdin = nil
}

func checkForUpdates() (bool, *string, error) {
	versionbytes, err := os.ReadFile("bedrock-server/version")
	if err != nil {
		utils.Fatal(err)
	}
	version := string(versionbytes)
	log.Printf("Current: %s", version)

	log.Println("Gathering latest minecraft version")
	resp, err := http.Get(minecraftnet)
	if err != nil {
		return false, nil, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false, nil, err
	}

	downloadUrl := dlregx.FindString(string(body))
	onlineversion := verregx.FindStringSubmatch(downloadUrl)[1]
	log.Printf("Available: %s", onlineversion)

	return version != onlineversion, &onlineversion, nil
}

func initializeDirectory(version string) {
	if err := os.Symlink(fmt.Sprintf("bedrock-server-%s", version), "bedrock-server"); err != nil {
		utils.Fatal("An error occurred while creating symlink to bedrock-server", err)
	}

	os.Chmod("bedrock-server/bedrock_server", 0755)

	symlink("worlds")
	symlink("server.properties")
	symlink("permissions.json")
	symlink("whitelist.json")
}

func install() *string {
	log.Println("Installing latest Minecraft Bedrock Server...")
	version, err := downloadBedrockServer()
	if err != nil {
		utils.Fatal("An error occurred while downloading the Minecraft Bedrock server", err)
	}
	initializeDirectory(*version)
	return version
}

func update() *string {
	log.Println("Checking for Minecraft Bedrock Server updates...")
	isUpdateAvailable, version, err := checkForUpdates()
	if err != nil {
		log.Println("An error occurred while checking for the latest version", err)
	}
	if !isUpdateAvailable {
		return version
	}
	log.Println("A newer version is available, downloading...")
	version, err = downloadBedrockServer()
	if err != nil {
		utils.Fatal("An error occurred while downloading the Minecraft Bedrock server", err)
	}
	if server != nil {
		updating = true
		log.Println("Stopping bedrock_server...")
		stop()
	}
	if _, err := os.Stat("bedrock-server"); err == nil {
		os.Remove("bedrock-server")
	}
	initializeDirectory(*version)
	return version
}

func Startup() error {
	var version *string
	if _, err := os.Stat("bedrock-server"); os.IsNotExist(err) {
		version = install()
	} else {
		// version = update()
		ver := "1.10.0.7"
		version = &ver
	}

	start(*version)

	return nil
}

func Shutdown(s os.Signal) error {
	log.Println("Stopping bedrock_server...")

	if s == syscall.SIGQUIT {
		go stop()
	} else {
		go terminate()
	}

	return nil
}

func Wait() {
	go func() {
		for {
			time.Sleep(time.Hour * time.Duration(6))
			version := update()
			if updating {
				start(*version)
				updating = false
			}
		}
	}()
	for {
		time.Sleep(time.Millisecond * time.Duration(500))
		if server == nil && !updating {
			os.Exit(0)
		}
	}
}

func HealthCheck() {
	discord.HealthChecked(true)
}
