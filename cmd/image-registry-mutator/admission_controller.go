/*
Copyright (c) 2019      StackRox Inc.
Copyright (c) 2019-2020 ZHAO Yao <ytinirt@qq.com>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"k8s.io/api/admission/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"log"
	"net/http"
)

const (
	jsonContentType = `application/json`
)

var (
	universalDeserializer = serializer.NewCodecFactory(runtime.NewScheme()).UniversalDeserializer()
)

// patchOperation is an operation of a JSON patch, see https://tools.ietf.org/html/rfc6902 .
type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

// admitFunc is a callback for admission controller logic. Given an AdmissionRequest, it returns the sequence of patch
// operations to be applied in case of success, or the error that will be shown when the operation is rejected.
type admitFunc func(*v1beta1.AdmissionRequest) ([]patchOperation, error)

// doServeAdmitFunc parses the HTTP request for an admission controller webhook, and -- in case of a well-formed
// request -- delegates the admission control logic to the given admitFunc. The response body is then returned as raw
// bytes.
func doServeAdmitFunc(w http.ResponseWriter, r *http.Request, admit admitFunc) ([]byte, error) {
	// Step 1: Request validation. Only handle POST requests with a body and json content type.

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil, fmt.Errorf("invalid method %s, only POST requests are allowed", r.Method)
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil, fmt.Errorf("could not read request body: %v", err)
	}

	if contentType := r.Header.Get("Content-Type"); contentType != jsonContentType {
		w.WriteHeader(http.StatusBadRequest)
		return nil, fmt.Errorf("unsupported content type %s, only %s is supported", contentType, jsonContentType)
	}

	// Step 2: Parse the AdmissionReview request.

	var admissionReviewReq v1beta1.AdmissionReview

	if _, _, err := universalDeserializer.Decode(body, nil, &admissionReviewReq); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil, fmt.Errorf("could not deserialize request: %v", err)
	} else if admissionReviewReq.Request == nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil, errors.New("malformed admission review: request is nil")
	}

	// Step 3: Construct the AdmissionReview response.

	admissionReviewResponse := v1beta1.AdmissionReview{
		Response: &v1beta1.AdmissionResponse{
			UID: admissionReviewReq.Request.UID,
		},
	}

	var patchOps []patchOperation
	patchOps, err = admit(admissionReviewReq.Request)

	if err != nil {
		// If the handler returned an error, incorporate the error message into the response and deny the object
		// creation.
		admissionReviewResponse.Response.Allowed = false
		admissionReviewResponse.Response.Result = &metav1.Status{
			Message: err.Error(),
		}
	} else {
		// Otherwise, encode the patch operations to JSON and return a positive response.
		patchBytes, err := json.Marshal(patchOps)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return nil, fmt.Errorf("could not marshal JSON patch: %v", err)
		}
		admissionReviewResponse.Response.Allowed = true
		admissionReviewResponse.Response.Patch = patchBytes
	}

	// Return the AdmissionReview with a response as JSON.
	bytes, err := json.Marshal(&admissionReviewResponse)
	if err != nil {
		return nil, fmt.Errorf("marshaling response: %v", err)
	}
	return bytes, nil
}

// serveAdmitFunc is a wrapper around doServeAdmitFunc that adds error handling and logging.
func serveAdmitFunc(w http.ResponseWriter, r *http.Request, admit admitFunc) {
	log.Print("Handling webhook request ...")

	var writeErr error
	if bytes, err := doServeAdmitFunc(w, r, admit); err != nil {
		log.Printf("Error handling webhook request: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, writeErr = w.Write([]byte(err.Error()))
	} else {
		log.Print("Webhook request handled successfully")
		_, writeErr = w.Write(bytes)
	}

	if writeErr != nil {
		log.Printf("Could not write response: %v", writeErr)
	}
}

// admitFuncHandler takes an admitFunc and wraps it into a http.Handler by means of calling serveAdmitFunc.
func admitFuncHandler(admit admitFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveAdmitFunc(w, r, admit)
	})
}
