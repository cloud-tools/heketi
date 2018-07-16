//
// Copyright (c) 2015 The heketi Authors
//
// This file is licensed to you under your choice of the GNU Lesser
// General Public License, version 3 or any later version (LGPLv3 or
// later), or the GNU General Public License, version 2 (GPLv2), in all
// cases as published by the Free Software Foundation.
//

package middleware

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jwt "github.com/dgrijalva/jwt-go"
	"github.com/gorilla/context"
	"github.com/cloud-tools/heketi/pkg/utils"
	"github.com/heketi/tests"
	"github.com/urfave/negroni"
)

func TestNewJwtAuth(t *testing.T) {
	c := &JwtAuthConfig{}
	c.Admin.PrivateKey = "Key"
	c.User.PrivateKey = "UserKey"

	j := NewJwtAuth(c)
	tests.Assert(t, string(j.adminKey) == c.Admin.PrivateKey)
	tests.Assert(t, string(j.userKey) == c.User.PrivateKey)
	tests.Assert(t, j != nil)
}

func TestNewJwtAuthFailure(t *testing.T) {
	c := &JwtAuthConfig{}
	j := NewJwtAuth(c)
	tests.Assert(t, j == nil)
}

func TestJwtNoToken(t *testing.T) {
	c := &JwtAuthConfig{}
	c.Admin.PrivateKey = "Key"
	c.User.PrivateKey = "UserKey"
	j := NewJwtAuth(c)
	tests.Assert(t, j != nil)

	n := negroni.New(j)
	tests.Assert(t, n != nil)

	called := false
	mw := func(rw http.ResponseWriter, r *http.Request) {
		called = true
	}
	n.UseHandlerFunc(mw)

	ts := httptest.NewServer(n)
	r, err := http.Get(ts.URL)
	tests.Assert(t, err == nil)
	tests.Assert(t, r.StatusCode == http.StatusUnauthorized)
	tests.Assert(t, called == false)
}

func TestJwtGarbageToken(t *testing.T) {

	// Setup jwt
	c := &JwtAuthConfig{}
	c.Admin.PrivateKey = "Key"
	c.User.PrivateKey = "UserKey"
	j := NewJwtAuth(c)
	tests.Assert(t, j != nil)

	// Setup middleware framework
	n := negroni.New(j)
	tests.Assert(t, n != nil)

	// Create a simple middleware to check if it was called
	called := false
	mw := func(rw http.ResponseWriter, r *http.Request) {
		called = true
	}
	n.UseHandlerFunc(mw)

	// Create test server
	ts := httptest.NewServer(n)

	// Setup header
	req, err := http.NewRequest("GET", ts.URL, nil)
	tests.Assert(t, err == nil)

	// Miss 'bearer' string
	req.Header.Set("Authorization", "123456770309238402938402398409234")

	// Call
	r, err := http.DefaultClient.Do(req)
	tests.Assert(t, err == nil)
	tests.Assert(t, r.StatusCode == http.StatusBadRequest)
	tests.Assert(t, called == false)

	s, err := utils.GetStringFromResponse(r)
	tests.Assert(t, err == nil)
	tests.Assert(t, strings.Contains(s, "Authorization header format must be Bearer"))

	// Setup header
	req, err = http.NewRequest("GET", ts.URL, nil)
	tests.Assert(t, err == nil)
	req.Header.Set("Authorization", "bearer")

	// Call
	r, err = http.DefaultClient.Do(req)
	tests.Assert(t, err == nil)
	tests.Assert(t, r.StatusCode == http.StatusBadRequest)
	tests.Assert(t, called == false)

	s, err = utils.GetStringFromResponse(r)
	tests.Assert(t, err == nil)
	tests.Assert(t, strings.Contains(s, "Authorization header format must be Bearer"))

	// Setup header
	req, err = http.NewRequest("GET", ts.URL, nil)
	tests.Assert(t, err == nil)
	req.Header.Set("Authorization", "bearer 123456770309238402938402398409234")

	// Call
	r, err = http.DefaultClient.Do(req)
	tests.Assert(t, err == nil)
	tests.Assert(t, r.StatusCode == http.StatusUnauthorized)
	tests.Assert(t, called == false)

	s, err = utils.GetStringFromResponse(r)
	tests.Assert(t, err == nil)
	tests.Assert(t, strings.Contains(s, "token contains an invalid number of segments"))

}

func TestJwtMissingClaims(t *testing.T) {
	// Setup jwt
	c := &JwtAuthConfig{}
	c.Admin.PrivateKey = "Key"
	c.User.PrivateKey = "UserKey"
	j := NewJwtAuth(c)
	tests.Assert(t, j != nil)

	// Setup middleware framework
	n := negroni.New(j)
	tests.Assert(t, n != nil)

	// Create a simple middleware to check if it was called
	called := false
	mw := func(rw http.ResponseWriter, r *http.Request) {
		called = true
	}
	n.UseHandlerFunc(mw)

	// Create test server
	ts := httptest.NewServer(n)

	// Create token with missing 'iss' claim
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": "admin",
	})
	tokenString, err := token.SignedString([]byte("Key"))
	tests.Assert(t, err == nil)

	// Setup header
	req, err := http.NewRequest("GET", ts.URL, nil)
	tests.Assert(t, err == nil)

	// Miss 'bearer' string
	req.Header.Set("Authorization", "bearer "+tokenString)
	r, err := http.DefaultClient.Do(req)
	tests.Assert(t, err == nil)
	tests.Assert(t, r.StatusCode == http.StatusBadRequest)
	tests.Assert(t, called == false)

	s, err := utils.GetStringFromResponse(r)
	tests.Assert(t, err == nil)
	tests.Assert(t, strings.Contains(s, "missing from token"))

}

func TestJwtInvalidToken(t *testing.T) {

	// Setup jwt
	c := &JwtAuthConfig{}
	c.Admin.PrivateKey = "Key"
	c.User.PrivateKey = "UserKey"
	j := NewJwtAuth(c)
	tests.Assert(t, j != nil)

	// Setup middleware framework
	n := negroni.New(j)
	tests.Assert(t, n != nil)

	// Create a simple middleware to check if it was called
	called := false
	mw := func(rw http.ResponseWriter, r *http.Request) {
		called = true
	}
	n.UseHandlerFunc(mw)

	// Create test server
	ts := httptest.NewServer(n)

	// Create token with missing 'iss' claim
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		// Set issued at time
		"iat": time.Now().Unix(),

		// Set expiration
		"exp": time.Now().Add(time.Second * 5).Unix(),
	})
	tokenString, err := token.SignedString([]byte("Key"))
	tests.Assert(t, err == nil)

	// Setup header
	req, err := http.NewRequest("GET", ts.URL, nil)
	tests.Assert(t, err == nil)

	// Miss 'bearer' string
	req.Header.Set("Authorization", "bearer "+tokenString)
	r, err := http.DefaultClient.Do(req)
	tests.Assert(t, err == nil)
	tests.Assert(t, r.StatusCode == http.StatusUnauthorized)
	tests.Assert(t, called == false)

	s, err := utils.GetStringFromResponse(r)
	tests.Assert(t, err == nil)
	tests.Assert(t, strings.Contains(s, "Token missing iss claim"))

	// Create an expired token
	token = jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		// Set issuer
		"iss": "admin",

		// Set issued at time
		"iat": time.Now().Unix(),

		// Set expiration
		"exp": time.Now().Add(time.Millisecond).Unix(),
	})
	tokenString, err = token.SignedString([]byte("Key"))
	tests.Assert(t, err == nil)

	// Wait a bit
	time.Sleep(time.Second)

	// Setup header
	req, err = http.NewRequest("GET", ts.URL, nil)
	tests.Assert(t, err == nil)

	// Send request
	req.Header.Set("Authorization", "bearer "+tokenString)
	r, err = http.DefaultClient.Do(req)
	tests.Assert(t, err == nil)
	tests.Assert(t, r.StatusCode == http.StatusUnauthorized)
	tests.Assert(t, called == false)

	s, err = utils.GetStringFromResponse(r)
	tests.Assert(t, err == nil)
	tests.Assert(t, strings.Contains(s, "Token is expired"), s)

	// Create missing 'qsh' claim
	token = jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		// Set issuer
		"iss": "admin",

		// Set issued at time
		"iat": time.Now().Unix(),

		// Set expiration
		"exp": time.Now().Add(time.Second * 10).Unix(),
	})
	tokenString, err = token.SignedString([]byte("Key"))
	tests.Assert(t, err == nil)

	// Setup header
	req, err = http.NewRequest("GET", ts.URL, nil)
	tests.Assert(t, err == nil)

	// Send request
	req.Header.Set("Authorization", "bearer "+tokenString)
	r, err = http.DefaultClient.Do(req)
	tests.Assert(t, err == nil)
	tests.Assert(t, r.StatusCode == http.StatusUnauthorized)
	tests.Assert(t, called == false)

	s, err = utils.GetStringFromResponse(r)
	tests.Assert(t, err == nil)
	tests.Assert(t, strings.Contains(s, "Invalid qsh claim in token"))

	// Create an invalid 'qsh' claim
	token = jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		// Set issuer
		"iss": "admin",

		// Set issued at time
		"iat": time.Now().Unix(),

		// Set expiration
		"exp": time.Now().Add(time.Second * 10).Unix(),

		// Set qsh
		"qsh": "12343345678945678a",
	})
	tokenString, err = token.SignedString([]byte("Key"))
	tests.Assert(t, err == nil)

	// Setup header
	req, err = http.NewRequest("GET", ts.URL, nil)
	tests.Assert(t, err == nil)

	// Send request
	req.Header.Set("Authorization", "bearer "+tokenString)
	r, err = http.DefaultClient.Do(req)
	tests.Assert(t, err == nil)
	tests.Assert(t, r.StatusCode == http.StatusUnauthorized)
	tests.Assert(t, called == false)

	s, err = utils.GetStringFromResponse(r)
	tests.Assert(t, err == nil)
	tests.Assert(t, strings.Contains(s, "Invalid qsh claim in token"))

}

func TestJwt(t *testing.T) {
	// Setup jwt
	c := &JwtAuthConfig{}
	c.Admin.PrivateKey = "Key"
	c.User.PrivateKey = "UserKey"
	j := NewJwtAuth(c)
	tests.Assert(t, j != nil)

	// Setup middleware framework
	n := negroni.New(j)
	tests.Assert(t, n != nil)

	// Create a simple middleware to check if it was called
	called := false
	mw := func(rw http.ResponseWriter, r *http.Request) {
		data := context.Get(r, "jwt")
		tests.Assert(t, data != nil)

		token := data.(*jwt.Token)
		claims := token.Claims.(jwt.MapClaims)
		tests.Assert(t, claims["iss"] == "admin")

		called = true

		rw.WriteHeader(http.StatusOK)
	}
	n.UseHandlerFunc(mw)

	// Create test server
	ts := httptest.NewServer(n)

	// Generate qsh
	qshstring := "GET&/"
	hash := sha256.New()
	hash.Write([]byte(qshstring))

	// Create token
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		// Set issuer
		"iss": "admin",

		// Set issued at time
		"iat": time.Now().Unix(),

		// Set expiration
		"exp": time.Now().Add(time.Second * 10).Unix(),

		// Set qsh
		"qsh": hex.EncodeToString(hash.Sum(nil)),
	})

	tokenString, err := token.SignedString([]byte("Key"))
	tests.Assert(t, err == nil)

	// Setup header
	req, err := http.NewRequest("GET", ts.URL, nil)
	tests.Assert(t, err == nil)

	// Miss 'bearer' string
	req.Header.Set("Authorization", "bearer "+tokenString)
	r, err := http.DefaultClient.Do(req)
	tests.Assert(t, err == nil)
	tests.Assert(t, r.StatusCode == http.StatusOK)
	tests.Assert(t, called == true)
}

func TestJwtUnknownUser(t *testing.T) {

	// Setup jwt
	c := &JwtAuthConfig{}
	c.Admin.PrivateKey = "Key"
	c.User.PrivateKey = "UserKey"
	j := NewJwtAuth(c)
	tests.Assert(t, j != nil)

	// Setup middleware framework
	n := negroni.New(j)
	tests.Assert(t, n != nil)

	// Create a simple middleware to check if it was called
	called := false
	mw := func(rw http.ResponseWriter, r *http.Request) {
		called = true
	}
	n.UseHandlerFunc(mw)

	// Create test server
	ts := httptest.NewServer(n)

	// Create token with invalid user
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		// Set issuer
		"iss": "someotheruser",

		// Set issued at time
		"iat": time.Now().Unix(),

		// Set expiration
		"exp": time.Now().Add(time.Second * 10).Unix(),
	})
	tokenString, err := token.SignedString([]byte("Key"))
	tests.Assert(t, err == nil)

	// Setup header
	req, err := http.NewRequest("GET", ts.URL, nil)
	tests.Assert(t, err == nil)

	// Miss 'bearer' string
	req.Header.Set("Authorization", "bearer "+tokenString)
	r, err := http.DefaultClient.Do(req)
	tests.Assert(t, err == nil)
	tests.Assert(t, r.StatusCode == http.StatusUnauthorized)
	tests.Assert(t, called == false)

	s, err := utils.GetStringFromResponse(r)
	tests.Assert(t, err == nil)
	tests.Assert(t, strings.Contains(s, "Unknown user"))
}

func TestJwtInvalidKeys(t *testing.T) {

	// Setup jwt
	c := &JwtAuthConfig{}
	c.Admin.PrivateKey = "Key"
	c.User.PrivateKey = "UserKey"
	j := NewJwtAuth(c)
	tests.Assert(t, j != nil)

	// Setup middleware framework
	n := negroni.New(j)
	tests.Assert(t, n != nil)

	// Create a simple middleware to check if it was called
	called := false
	mw := func(rw http.ResponseWriter, r *http.Request) {
		called = true
	}
	n.UseHandlerFunc(mw)

	// Create test server
	ts := httptest.NewServer(n)

	// Invalid user key
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		// Set issuer
		"iss": "user",

		// Set issued at time
		"iat": time.Now().Unix(),

		// Set expiration
		"exp": time.Now().Add(time.Second * 10).Unix(),
	})
	tokenString, err := token.SignedString([]byte("Badkey"))
	tests.Assert(t, err == nil)

	// Setup header
	req, err := http.NewRequest("GET", ts.URL, nil)
	tests.Assert(t, err == nil)

	// Miss 'bearer' string
	req.Header.Set("Authorization", "bearer "+tokenString)
	r, err := http.DefaultClient.Do(req)
	tests.Assert(t, err == nil)
	tests.Assert(t, r.StatusCode == http.StatusUnauthorized)
	tests.Assert(t, called == false)

	s, err := utils.GetStringFromResponse(r)
	tests.Assert(t, err == nil)
	tests.Assert(t, strings.Contains(s, "signature is invalid"))

	// Send invalid admin key
	token = jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		// Set issuer
		"iss": "admin",

		// Set issued at time
		"iat": time.Now().Unix(),

		// Set expiration
		"exp": time.Now().Add(time.Second * 10).Unix(),
	})
	tokenString, err = token.SignedString([]byte("Badkey"))
	tests.Assert(t, err == nil)

	// Setup header
	req, err = http.NewRequest("GET", ts.URL, nil)
	tests.Assert(t, err == nil)

	// Miss 'bearer' string
	req.Header.Set("Authorization", "bearer "+tokenString)
	r, err = http.DefaultClient.Do(req)
	tests.Assert(t, err == nil)
	tests.Assert(t, r.StatusCode == http.StatusUnauthorized)
	tests.Assert(t, called == false)

	s, err = utils.GetStringFromResponse(r)
	tests.Assert(t, err == nil)
	tests.Assert(t, strings.Contains(s, "signature is invalid"))
}

// TestJwtWrongSigningMethod tests the error condition triggered
// by the use of any signing method other than HMAC + SHA256
// since that is the only signing method heketi supports.
// The content is a _valid_ jwt, just not one heketi can accept.
func TestJwtWrongSigningMethod(t *testing.T) {
	// Setup jwt
	c := &JwtAuthConfig{}
	c.Admin.PrivateKey = "Key"
	c.User.PrivateKey = "UserKey"
	j := NewJwtAuth(c)
	tests.Assert(t, j != nil, "NewJwtAuth failed")

	// Setup middleware framework
	n := negroni.New(j)
	tests.Assert(t, n != nil, "negroni.New failed")

	// Create a simple middleware to check if it was called
	called := false
	mw := func(rw http.ResponseWriter, r *http.Request) {
		data := context.Get(r, "jwt")
		tests.Assert(t, data != nil, "context.Get failed")

		token := data.(*jwt.Token)
		claims := token.Claims.(jwt.MapClaims)
		tests.Assert(t, claims["iss"] == "admin",
			`expected claims["iss"] == "admin", got:`, claims["iss"])

		called = true

		rw.WriteHeader(http.StatusOK)
	}
	n.UseHandlerFunc(mw)

	// Create test server
	ts := httptest.NewServer(n)

	// Instead of creating a token with the H256 suffix (HMAC+SHA256)
	// we use SigningMethodPS256 (RSASSA-PSS) for no particular reason
	// other than its not H256.
	token := jwt.NewWithClaims(jwt.SigningMethodPS256, jwt.MapClaims{
		// Set issuer
		"iss": "admin",

		// Set issued at time
		"iat": time.Now().Unix(),

		// Set expiration
		"exp": time.Now().Add(time.Second * 10).Unix(),
	})

	// Setup pre-req bits needed to make our PS256 valid.
	// Should we use a fake source of randomness instead of real
	// rand.Reader here?
	pk, err := rsa.GenerateKey(rand.Reader, 256*2)
	tests.Assert(t, err == nil, "rsa.GenerateKey failed:", err)
	tokenString, err := token.SignedString(pk)
	tests.Assert(t, err == nil, "token.SignedString failed:", err)

	// Setup header
	req, err := http.NewRequest("GET", ts.URL, nil)
	tests.Assert(t, err == nil, "http.NewRequest failed:", err)

	// confirm that when we pass this token to the server it
	// fails with an error message that says the signing method
	// we provided is unexpected.
	req.Header.Set("Authorization", "bearer "+tokenString)
	r, err := http.DefaultClient.Do(req)
	tests.Assert(t, err == nil, "http.DefaultClient failed:", err)
	tests.Assert(t, r.StatusCode != 0)
	s, err := utils.GetStringFromResponse(r)
	tests.Assert(t, strings.Contains(s, "Unexpected signing method"),
		`expected s to contain "Unexpected signing method", got:`, s)
}
