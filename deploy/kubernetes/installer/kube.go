// Copyright 2026 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// kubeClient is a minimal in-cluster Kubernetes API client. It avoids a
// dependency on client-go to keep the installer image small.
type kubeClient struct {
	base   string
	client *http.Client
}

func newKubeClient() (*kubeClient, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("not running in a Kubernetes cluster")
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("no certificates in %s/ca.crt", saDir)
	}
	return &kubeClient{
		base: "https://" + net.JoinHostPort(host, port),
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			},
		},
	}, nil
}

func (k *kubeClient) do(method, path, contentType string, body any, out any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, k.base+path, r)
	if err != nil {
		return err
	}
	// Read the token on each request: projected tokens rotate.
	token, err := os.ReadFile(saDir + "/token")
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+string(bytes.TrimSpace(token)))
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, data)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// setNodeLabel sets label key on node to value. A nil value removes it.
func (k *kubeClient) setNodeLabel(node, key string, value *string) error {
	patch := map[string]any{"metadata": map[string]any{"labels": map[string]any{key: value}}}
	return k.do(http.MethodPatch, "/api/v1/nodes/"+node, "application/merge-patch+json", patch, nil)
}

type daemonSetStatus struct {
	Metadata struct {
		Generation int64 `json:"generation"`
	} `json:"metadata"`
	Status struct {
		ObservedGeneration     int64 `json:"observedGeneration"`
		DesiredNumberScheduled int64 `json:"desiredNumberScheduled"`
		UpdatedNumberScheduled int64 `json:"updatedNumberScheduled"`
		NumberReady            int64 `json:"numberReady"`
		NumberUnavailable      int64 `json:"numberUnavailable"`
	} `json:"status"`
}

func (k *kubeClient) patchDaemonSet(ns, name string, patch any) error {
	return k.do(http.MethodPatch, fmt.Sprintf("/apis/apps/v1/namespaces/%s/daemonsets/%s", ns, name), "application/merge-patch+json", patch, nil)
}

func (k *kubeClient) getDaemonSet(ns, name string) (*daemonSetStatus, error) {
	var ds daemonSetStatus
	err := k.do(http.MethodGet, fmt.Sprintf("/apis/apps/v1/namespaces/%s/daemonsets/%s", ns, name), "", nil, &ds)
	return &ds, err
}
