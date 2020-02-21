// Copyright 2020 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-redis/redis"
)

const (
	TlsDir      string = "/etc/webhook/certs"
	TlsCertFile string = "cert.pem"
	TlsKeyFile  string = "key.pem"
)

const (
	MutateApi        string = "/mutate"
	WebhookPort      string = ":8443"
	DefaultRedisHost string = "localhost"
	DefaultRedisPort string = "6379"
)

var (
	redisHost     = getEnv("REDIS_HOST", DefaultRedisHost)
	redisPort     = getEnv("REDIS_PORT", DefaultRedisPort)
	redisPassword = ""
)

func main() {
	certPath := filepath.Join(TlsDir, TlsCertFile)
	keyPath := filepath.Join(TlsDir, TlsKeyFile)

	mux := http.NewServeMux()
	mux.Handle(MutateApi, admitFuncHandler(mutatePodIfCached))
	server := &http.Server{
		// We listen on port 8443 such that we do not need root privileges or extra capabilities for this server.
		// The Service object will take care of mapping this port to the HTTPS port 443.
		Addr:    WebhookPort,
		Handler: mux,
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr:     redisHost + ":" + redisPort,
		Password: redisPassword,
		DB:       0,
	})

	// Test redis
	err := redisClient.Set("testKey", "testValue", 0).Err()
	if err != nil {
		panic(err)
	}
	val, _ := redisClient.Get("testKey").Result()
	log.Println(val)
	pong, err := redisClient.Ping().Result()
	log.Println(pong, err)

	log.Fatal(server.ListenAndServeTLS(certPath, keyPath))
}

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
