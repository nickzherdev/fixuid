package main

import (
	"bufio"
	"errors"
	"io/ioutil"
	"log"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/go-ozzo/ozzo-config"
)

var logger = log.New(os.Stderr, "", 0)

func main() {
	logger.SetPrefix("fixuid: ")

	// load config from /etc/fixuid/config.[json|toml|yaml|yml]
	rootConfig := config.New()
	configError := errors.New("could not find config at /etc/fixuid/config.[json|toml|yaml|yml]")
	var filePath string
	for _, fileName := range [...]string{"config.json", "config.toml", "config.yaml", "config.yml"} {
		filePath = path.Join("/etc/fixuid", fileName)
		if _, err := os.Stat(filePath); !os.IsNotExist(err) {
			configError = rootConfig.Load(filePath)
			if configError != nil {
				logger.Println("error when loading configuration file " + filePath)
			} else {
				break
			}
		}
	}
	if configError != nil {
		logger.Fatalln(configError)
	}

	// validate the container user from the config
	containerUser := rootConfig.GetString("user")
	if containerUser == "" {
		logger.Fatalln("cannot find key 'user' in configuration file " + filePath)
	}
	containerUID, containerUIDError := findUID(containerUser)
	if containerUIDError != nil {
		logger.Fatalln(containerUIDError)
	}
	if containerUID == "" {
		logger.Fatalln("user '" + containerUser + "' does not exist")
	}
	containerUIDInt, err := strconv.Atoi(containerUID)
	if err != nil {
		logger.Fatal(err)
	}
	containerUIDUint32 := uint32(containerUIDInt)

	// validate the container group from the config
	containerGroup := rootConfig.GetString("group")
	if containerGroup == "" {
		logger.Fatalln("cannot find key 'group' in configuration file " + filePath)
	}
	containerGID, containerGIDError := findGID(containerGroup)
	if containerGIDError != nil {
		logger.Fatalln(containerGIDError)
	}
	if containerGID == "" {
		logger.Fatalln("group '" + containerGroup + "' does not exist")
	}
	containerGIDInt, err := strconv.Atoi(containerGID)
	if err != nil {
		logger.Fatal(err)
	}
	containerGIDUint32 := uint32(containerGIDInt)

	// detect what user we are running as
	runtimeUIDInt := os.Getuid()
	runtimeUID := strconv.Itoa(runtimeUIDInt)
	runtimeGIDInt := os.Getgid()
	runtimeGID := strconv.Itoa(runtimeGIDInt)

	// declare uid/gid vars and
	var oldUID, newUID, oldGID, newGID string
	needChown := false

	// decide if need to change UIDs
	existingUser, existingUserError := findUser(runtimeUID)
	if existingUserError != nil {
		logger.Fatalln(existingUserError)
	}
	if existingUser == "" {
		logger.Println("updating user '" + containerUser + "' to UID '" + runtimeUID)
		needChown = true
		oldUID = containerUID
		newUID = runtimeUID
	} else {
		oldUID = ""
		newUID = ""
		if existingUser != containerUser {
			logger.Println("runtime UID '" + runtimeUID + "' matches existing user '" + existingUser + "'; not changing UID")
			needChown = true
		}
	}

	// deicide if need to change GIDs
	existingGroup, existingGroupError := findGroup(runtimeGID)
	if existingGroupError != nil {
		logger.Fatalln(existingGroupError)
	}
	if existingGroup == "" {
		logger.Println("updating group '" + containerGroup + "' to GID '" + runtimeGID)
		needChown = true
		oldGID = containerGID
		newGID = runtimeGID
	} else {
		oldGID = ""
		newGID = ""
		if existingGroup != containerGroup {
			logger.Println("runtime GID '" + runtimeGID + "' matches existing group '" + existingGroup + "'; not changing GID")
			needChown = true
		}
	}

	// update /etc/passwd if necessary
	if oldUID != newUID || oldGID != newGID {
		err := updateEtcPasswd(containerUser, oldUID, newUID, oldGID, newGID)
		if err != nil {
			logger.Fatalln(err)
		}
	}

	// update /etc/group if necessary
	if oldGID != newGID {
		err := updateEtcGroup(containerGroup, oldGID, newGID)
		if err != nil {
			logger.Fatalln(err)
		}
	}

	// search entire filesystem and chown containerUID:containerGID to runtimeUID:runtimeGID
	if needChown {

		// stat / to determine device
		rootInfo, err := os.Stat("/")
		if err != nil {
			logger.Fatal(err)
		}
		sys, ok := rootInfo.Sys().(*syscall.Stat_t)
		if !ok {
			logger.Fatal("Cannot stat /")
		}
		rootDev := sys.Dev

		// this function is called for every file visited
		visit := func(filePath string, fileInfo os.FileInfo, err error) error {

			// stat file to determine UID, GID, and device
			sys, ok := fileInfo.Sys().(*syscall.Stat_t)
			if !ok {
				logger.Println("Cannot stat " + filePath)
				return filepath.SkipDir
			}

			// prevent recursing into mounts - skip if it is not the same device as /
			if sys.Dev != rootDev {
				return filepath.SkipDir
			}

			// only chown if file is containerUID:containerGID
			if sys.Uid == containerUIDUint32 && sys.Gid == containerGIDUint32 {
				logger.Println("chown " + filePath)
				chownError := syscall.Chown(filePath, runtimeUIDInt, runtimeGIDInt)
				if chownError != nil {
					logger.Println("error changing owner of " + filePath)
					logger.Println(err)
				}
				return chownError
			}
			return nil
		}

		filepath.Walk("/", visit)
	}

	// all done
	os.Exit(0)
}

func searchColonDelimetedFile(filePath string, search string, searchOffset int, returnOffset int) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		cols := strings.Split(scanner.Text(), ":")
		if cols[searchOffset] == search {
			return cols[returnOffset], nil
		}
	}
	return "", nil
}

func findUID(user string) (string, error) {
	return searchColonDelimetedFile("/etc/passwd", user, 0, 2)
}

func findUser(uid string) (string, error) {
	return searchColonDelimetedFile("/etc/passwd", uid, 2, 0)
}

func findGID(group string) (string, error) {
	return searchColonDelimetedFile("/etc/group", group, 0, 2)
}

func findGroup(gid string) (string, error) {
	return searchColonDelimetedFile("/etc/group", gid, 2, 0)
}

func updateEtcPasswd(user string, oldUID string, newUID string, oldGID string, newGID string) error {
	// user:pass:uid:gid:comment:home dir:shell
	file, err := os.Open("/etc/passwd")
	if err != nil {
		return err
	}

	newLines := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		cols := strings.Split(scanner.Text(), ":")
		if oldUID != "" && newUID != "" && cols[0] == user && cols[2] == oldUID {
			cols[2] = newUID
		}
		if oldGID != "" && newGID != "" && cols[3] == oldGID {
			cols[3] = newGID
		}
		newLines += strings.Join(cols, ":") + "\n"
	}
	file.Close()

	if err := scanner.Err(); err != nil {
		return err
	}

	if err := ioutil.WriteFile("/etc/passwd", []byte(newLines), 0644); err != nil {
		return err
	}

	return nil
}

func updateEtcGroup(group string, oldGID string, newGID string) error {
	// group:pass:gid:users
	file, err := os.Open("/etc/group")
	if err != nil {
		return err
	}

	newLines := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		cols := strings.Split(scanner.Text(), ":")
		if oldGID != "" && newGID != "" && cols[0] == group && cols[2] == oldGID {
			cols[2] = newGID
		}
		newLines += strings.Join(cols, ":") + "\n"
	}
	file.Close()

	if err := scanner.Err(); err != nil {
		return err
	}

	if err := ioutil.WriteFile("/etc/group", []byte(newLines), 0644); err != nil {
		return err
	}

	return nil
}