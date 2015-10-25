package server

import (
	"golang.org/x/crypto/nacl/box"
	"net/http"
	"time"
	"fmt"
	"net/url"
	"github.com/pmylund/go-cache"
	"strconv"
	"io/ioutil"
	"encoding/json"
	"sync"
	"log"
	"os"
	"crypto/rand"
	"encoding/base64"
	"strings"
)

var backendHttpClient http.Client
var backendUrl string
var responseCache *cache.Cache

var getBacklogUrl string

var backendSharedKey [32]byte

var messageBufferPool sync.Pool

func SetupBackend(config *Config) {
	backendHttpClient.Timeout = 60 * time.Second
	backendUrl = config.BackendUrl
	if responseCache != nil {
		responseCache.Flush()
	}
	responseCache = cache.New(60 * time.Second, 120 * time.Second)

	getBacklogUrl = fmt.Sprintf("%s/backlog", backendUrl)

	messageBufferPool.New = NewByteBuffer

	var keys CryptoKeysBuf
	file, err := os.Open(config.NaclKeysFile)
	if err != nil {
		log.Fatal(err)
	}
	dec := json.NewDecoder(file)
	err = dec.Decode(&keys)
	if err != nil {
		log.Fatal(err)
	}

	box.Precompute(&backendSharedKey, &keys.TheirPublicKey, &keys.OurPrivateKey)
}

func getCacheKey(remoteCommand, data string) string {
	return fmt.Sprintf("%s/%s", remoteCommand, data)
}

func SealRequest(form url.Values) ([]byte, error) {
	asString := form.Encode()
	var nonce [24]byte
	var err error

	err = FillCryptoRandom(nonce[:])
	if err != nil {
		return nil, err
	}

	message := []byte(asString)
	out := make([]byte, len(message) + box.Overhead)
	box.SealAfterPrecomputation(out, message, &nonce, &backendSharedKey)

	// TODO
	return nil, nil
}

func RequestRemoteDataCached(remoteCommand, data string, auth AuthInfo) (string, error) {
	cached, ok := responseCache.Get(getCacheKey(remoteCommand, data))
	if ok {
		return cached.(string), nil
	}
	return RequestRemoteData(remoteCommand, data, auth)
}

func RequestRemoteData(remoteCommand, data string, auth AuthInfo) (responseStr string, err error) {
	destUrl := fmt.Sprintf("%s/cmd/%s", backendUrl, remoteCommand)
	var authKey string
	if auth.UsernameValidated {
		authKey = "usernameClaimed"
	} else {
		authKey = "username"
	}

	formData := url.Values{
		"clientData": []string{data},
		authKey: []string{auth.TwitchUsername},
	}

	resp, err := backendHttpClient.PostForm(destUrl, formData)
	if err != nil {
		return "", err
	}

	respBytes, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return "", err
	}

	responseStr = string(respBytes)

	if resp.Header.Get("FFZ-Cache") != "" {
		durSecs, err := strconv.ParseInt(resp.Header.Get("FFZ-Cache"), 10, 64)
		if err != nil {
			return "", fmt.Errorf("The RPC server returned a non-integer cache duration: %v", err)
		}
		duration := time.Duration(durSecs) * time.Second
		responseCache.Set(getCacheKey(remoteCommand, data), responseStr, duration)
	}

	return
}

func FetchBacklogData(chatSubs, channelSubs []string) ([]ClientMessage, error) {
	formData := url.Values{
		"chatSubs": chatSubs,
		"channelSubs": channelSubs,
	}

	resp, err := backendHttpClient.PostForm(getBacklogUrl, formData)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(resp.Body)
	var messages []ClientMessage
	err = dec.Decode(messages)
	if err != nil {
		return nil, err
	}

	return messages, nil
}

func GenerateKeys(outputFile, serverId, theirPublicStr string) {
	var err error
	output := CryptoKeysBuf{}

	output.ServerId, err = strconv.Atoi(serverId)
	if err != nil {
		log.Fatal(err)
	}

	ourPublic, ourPrivate, err := box.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	output.OurPublicKey, output.OurPrivateKey = *ourPublic, *ourPrivate

	if theirPublicStr != "" {
		reader := base64.NewDecoder(base64.RawURLEncoding, strings.NewReader(theirPublicStr))
		theirPublic, err := ioutil.ReadAll(reader)
		if err != nil {
			log.Fatal(err)
		}
		copy(output.TheirPublicKey[:], theirPublic)
	}

	file, err := os.Create(outputFile)
	if err != nil {
		log.Fatal(err)
	}
	enc := json.NewEncoder(file)
	err = enc.Encode(output)
	if err != nil {
		log.Fatal(err)
	}
	err = file.Close()
	if err != nil {
		log.Fatal(err)
	}
}
