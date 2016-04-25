// Copyright (c) 2016, German Neuroinformatics Node (G-Node),
//                     Adrian Stoewer <adrian.stoewer@rz.ifi.lmu.de>
// All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted under the terms of the BSD License. See
// LICENSE file in the root of the Project.

package web

// TODO Extend existing approvals
// TODO Add session cookie for SSO like feature

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/G-Node/gin-auth/data"
	"github.com/G-Node/gin-auth/util"
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

// Authorize handles the beginning of an OAuth grant request following the schema
// of 'implicit' or 'code' grant types.
func Authorize(w http.ResponseWriter, r *http.Request) {
	param := &struct {
		ResponseType string
		ClientId     string
		RedirectURI  string
		State        string
		Scope        []string
	}{}

	err := util.ReadQueryIntoStruct(r, param, false)
	if err != nil {
		PrintErrorHTML(w, r, err, 400)
		return
	}

	client, ok := data.GetClientByName(param.ClientId)
	if !ok {
		PrintErrorHTML(w, r, fmt.Sprintf("Client '%s' does not exist", param.ClientId), http.StatusBadRequest)
		return
	}

	scope := util.NewStringSet(param.Scope...)
	request, err := client.CreateGrantRequest(param.ResponseType, param.RedirectURI, param.State, scope)
	if err != nil {
		PrintErrorHTML(w, r, err, http.StatusBadRequest)
		return
	}

	w.Header().Add("Cache-Control", "no-store")
	http.Redirect(w, r, "/oauth/login_page?request_id="+request.Token, http.StatusFound)
}

type loginData struct {
	Login     string
	Password  string
	RequestID string
}

// LoginPage shows a page where the user can enter his credentials.
func LoginPage(w http.ResponseWriter, r *http.Request) {
	data.ClearOldGrantRequests()

	query := r.URL.Query()
	if query == nil {
		PrintErrorHTML(w, r, "Query parameter 'request_id' was missing", http.StatusBadRequest)
		return
	}
	token := query.Get("request_id")

	_, ok := data.GetGrantRequest(token)
	if !ok {
		PrintErrorHTML(w, r, "Grant request does not exist", http.StatusNotFound)
		return
	}

	tmpl, err := template.ParseFiles("assets/html/layout.html", "assets/html/login.html")
	if err != nil {
		panic(err)
	}

	w.Header().Add("Cache-Control", "no-store")
	w.Header().Add("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(w, "layout", &loginData{RequestID: token})
	if err != nil {
		panic(err)
	}
}

// Login validates user credentials.
func Login(w http.ResponseWriter, r *http.Request) {
	data.ClearOldGrantRequests()

	loginParam := &loginData{}
	err := util.ReadFormIntoStruct(r, loginParam, false)
	if err != nil {
		PrintErrorHTML(w, r, err, http.StatusBadRequest)
		return
	}

	// look for existing grant request
	request, ok := data.GetGrantRequest(loginParam.RequestID)
	if !ok {
		PrintErrorHTML(w, r, "Grant request does not exist", http.StatusNotFound)
		return
	}

	// verify login data
	account, ok := data.GetAccountByLogin(loginParam.Login)
	if !ok {
		w.Header().Add("Cache-Control", "no-store")
		http.Redirect(w, r, "/oauth/login_page?request_id="+request.Token, http.StatusUnauthorized)
		return
	}

	ok = account.VerifyPassword(loginParam.Password)
	if !ok {
		w.Header().Add("Cache-Control", "no-store")
		http.Redirect(w, r, "/oauth/login_page?request_id="+request.Token, http.StatusUnauthorized)
		return
	}

	request.AccountUUID = sql.NullString{String: account.UUID, Valid: true}
	err = request.Update()
	if err != nil {
		panic(err)
	}

	// if approved finish the grant request, otherwise redirect to approve page
	approved := request.ApproveScopes()
	if approved {
		if request.GrantType == "code" {
			finishCodeRequest(w, r, request)
		} else {
			finishImplicitRequest(w, r, request)
		}
	} else {
		w.Header().Add("Cache-Control", "no-store")
		http.Redirect(w, r, "/oauth/approve_page?request_id="+request.Token, http.StatusFound)
	}
}

func finishCodeRequest(w http.ResponseWriter, r *http.Request, request *data.GrantRequest) {
	request.Code = sql.NullString{String: util.RandomToken(), Valid: true}
	err := request.Update()
	if err != nil {
		panic(err)
	}

	scope := url.QueryEscape(strings.Join(request.ScopeApproved.Strings(), ","))
	state := url.QueryEscape(request.State)
	url := fmt.Sprintf("%s?scope=%s&state=%s&code=%s", request.RedirectURI, scope, state, request.Code.String)

	w.Header().Add("Cache-Control", "no-store")
	http.Redirect(w, r, url, http.StatusFound)
}

func finishImplicitRequest(w http.ResponseWriter, r *http.Request, request *data.GrantRequest) {
	err := request.Delete()
	if err != nil {
		panic(err)
	}

	token := &data.AccessToken{
		Token:       util.RandomToken(),
		ClientUUID:  request.ClientUUID,
		AccountUUID: request.AccountUUID.String,
		Scope:       request.ScopeApproved,
	}

	err = token.Create()
	if err != nil {
		panic(err)
	}

	scope := url.QueryEscape(strings.Join(token.Scope.Strings(), ","))
	state := url.QueryEscape(request.State)
	url := fmt.Sprintf("%s?token_type=bearer&scope=%s&state=%s&access_token=%s", request.RedirectURI, scope, state, token.Token)

	w.Header().Add("Cache-Control", "no-store")
	http.Redirect(w, r, url, http.StatusFound)
}

type approveData struct {
	Client    string
	RequestID string
	Scope     []string
}

// ApprovePage shows a page where the user can approve client access.
func ApprovePage(w http.ResponseWriter, r *http.Request) {
	data.ClearOldGrantRequests()

	query := r.URL.Query()
	if query == nil {
		PrintErrorHTML(w, r, "Query parameter 'request_id' was missing", http.StatusBadRequest)
		return
	}
	token := query.Get("request_id")

	request, ok := data.GetGrantRequest(token)
	if !ok {
		PrintErrorHTML(w, r, "Grant request does not exist", http.StatusNotFound)
		return
	}
	client, ok := data.GetClient(request.ClientUUID)
	if !ok {
		panic("Client does not exist")
	}

	description, ok := data.DescribeScope(request.ScopeRequested)
	if !ok {
		panic("Invalid scope")
	}
	pageData := struct {
		Client    string
		Scope     map[string]string
		RequestID string
	}{client.Name, description, request.Token}

	tmpl, err := template.ParseFiles("assets/html/layout.html", "assets/html/approve.html")
	if err != nil {
		panic(err)
	}

	w.Header().Add("Cache-Control", "no-store")
	w.Header().Add("Content-Type", "text/html")
	err = tmpl.ExecuteTemplate(w, "layout", pageData)
	if err != nil {
		panic(err)
	}
}

// Approve evaluates an access approval given to a certain client.
func Approve(w http.ResponseWriter, r *http.Request) {
	data.ClearOldGrantRequests()

	approveParam := &approveData{}
	util.ReadFormIntoStruct(r, approveParam, true)

	request, ok := data.GetGrantRequest(approveParam.RequestID)
	if !ok {
		PrintErrorHTML(w, r, "Grant request does not exist", http.StatusNotFound)
		return
	}

	// create approval
	approval := &data.ClientApproval{
		Scope:       util.NewStringSet(approveParam.Scope...),
		ClientUUID:  request.ClientUUID,
		AccountUUID: request.AccountUUID.String,
	}
	err := approval.Create()
	if err != nil {
		panic(err)
	}

	// if approved finish the grant request
	approved := request.ApproveScopes()
	if !approved {
		panic("Unable to approve requested scope")
	}

	if request.GrantType == "code" {
		finishCodeRequest(w, r, request)
	} else {
		finishImplicitRequest(w, r, request)
	}
}

type requestTokenData struct {
	RedirectURI string
	Code        string
	GrantType   string
}

type responseTokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
}

// Token exchanges a grant code for an access and refresh token
func Token(w http.ResponseWriter, r *http.Request) {
	data.ClearOldGrantRequests()

	clientName, clientSecret, ok := r.BasicAuth()
	if !ok {
		PrintErrorJSON(w, r, "No credentials provided", http.StatusUnauthorized)
		return
	}

	client, ok := data.GetClientByName(clientName)
	if !ok {
		PrintErrorJSON(w, r, "Wrong client id or client secret", http.StatusUnauthorized)
		return
	}
	if clientSecret != client.Secret {
		PrintErrorJSON(w, r, "Wrong client id or client secret", http.StatusUnauthorized)
		return
	}

	requestData := &requestTokenData{}
	err := util.ReadFormIntoStruct(r, requestData, false)
	if err != nil {
		PrintErrorJSON(w, r, err, 400)
		return
	}
	if requestData.GrantType != "authorization_code" {
		PrintErrorJSON(w, r, "Unsupported grant type", http.StatusBadRequest)
		return
	}

	request, ok := data.GetGrantRequestByCode(requestData.Code)
	if !ok {
		PrintErrorJSON(w, r, "Invalid grant code", http.StatusUnauthorized)
		return
	}

	access, refresh, err := request.ExchangeCodeForTokens()
	if err != nil {
		PrintErrorJSON(w, r, "Invalid grant code", http.StatusUnauthorized)
		return
	}

	response := responseTokenData{
		TokenType:    "Bearer",
		AccessToken:  access,
		RefreshToken: refresh}

	w.Header().Add("Cache-Control", "no-cache")
	w.Header().Add("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.Encode(response)
}
