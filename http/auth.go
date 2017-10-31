package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/dgrijalva/jwt-go/request"
	fm "github.com/hacdias/filemanager"
)

const reCaptchaAPI = "https://www.google.com/recaptcha/api/siteverify"

type cred struct {
	Password  string `json:"password"`
	Username  string `json:"username"`
	ReCaptcha string `json:"recaptcha"`
}

// reCaptcha checks the reCaptcha code.
func reCaptcha(secret string, response string) (bool, error) {
	body := url.Values{}
	body.Set("secret", secret)
	body.Add("response", response)

	client := &http.Client{}

	resp, err := client.Post(reCaptchaAPI, "application/x-www-form-urlencoded", strings.NewReader(body.Encode()))
	if err != nil {
		return false, err
	}

	if resp.StatusCode != http.StatusOK {
		return false, nil
	}

	var data struct {
		Success     bool        `json:"success"`
		ChallengeTS time.Time   `json:"challenge_ts"`
		Hostname    string      `json:"hostname"`
		ErrorCodes  interface{} `json:"error-codes"`
	}

	err = json.NewDecoder(resp.Body).Decode(&data)
	if err != nil {
		return false, err
	}

	return data.Success, nil
}

// authHandler processes the authentication for the user.
func authHandler(c *fm.Context, w http.ResponseWriter, r *http.Request) (int, error) {
	// NoAuth instances shouldn't call this method.
	if c.NoAuth {
		return 0, nil
	}

	// Receive the credentials from the request and unmarshal them.
	var cred cred
	if r.Body == nil {
		return http.StatusForbidden, nil
	}

	err := json.NewDecoder(r.Body).Decode(&cred)
	if err != nil {
		return http.StatusForbidden, nil
	}

	// If ReCaptcha is enabled, check the code.
	if len(c.ReCaptchaSecret) > 0 {
		ok, err := reCaptcha(c.ReCaptchaSecret, cred.ReCaptcha)
		if err != nil {
			fmt.Println(err)
			return http.StatusForbidden, err
		}

		if !ok {
			return http.StatusForbidden, nil
		}
	}

	// Checks if the user exists.
	u, err := c.Store.Users.GetByUsername(cred.Username, c.NewFS)
	if err != nil {
		return http.StatusForbidden, nil
	}

	// Checks if the password is correct.
	if !fm.CheckPasswordHash(cred.Password, u.Password) {
		return http.StatusForbidden, nil
	}

	c.User = u
	return printToken(c, w)
}

// renewAuthHandler is used when the front-end already has a JWT token
// and is checking if it is up to date. If so, updates its info.
func renewAuthHandler(c *fm.Context, w http.ResponseWriter, r *http.Request) (int, error) {
	ok, u := validateAuth(c, r)
	if !ok {
		return http.StatusForbidden, nil
	}

	c.User = u
	return printToken(c, w)
}

// claims is the JWT claims.
type claims struct {
	fm.User
	jwt.StandardClaims
}

// printToken prints the final JWT token to the user.
func printToken(c *fm.Context, w http.ResponseWriter) (int, error) {
	// Creates a copy of the user and removes it password
	// hash so it never arrives to the user.
	u := fm.User{}
	u = *c.User
	u.Password = ""

	// Builds the claims.
	claims := claims{
		u,
		jwt.StandardClaims{
			ExpiresAt: time.Now().Add(time.Hour * 24).Unix(),
			Issuer:    "File Manager",
		},
	}

	// Creates the token and signs it.
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(c.Key)

	if err != nil {
		return http.StatusInternalServerError, err
	}

	// Writes the token.
	w.Header().Set("Content-Type", "cty")
	w.Write([]byte(signed))
	return 0, nil
}

type extractor []string

func (e extractor) ExtractToken(r *http.Request) (string, error) {
	token, _ := request.AuthorizationHeaderExtractor.ExtractToken(r)

	// Checks if the token isn't empty and if it contains two dots.
	// The former prevents incompatibility with URLs that previously
	// used basic auth.
	if token != "" && strings.Count(token, ".") == 2 {
		return token, nil
	}

	cookie, err := r.Cookie("auth")
	if err != nil {
		return "", request.ErrNoTokenInRequest
	}

	return cookie.Value, nil
}

// validateAuth is used to validate the authentication and returns the
// User if it is valid.
func validateAuth(c *fm.Context, r *http.Request) (bool, *fm.User) {
	if c.NoAuth {
		c.User = c.DefaultUser
		return true, c.User
	}

	keyFunc := func(token *jwt.Token) (interface{}, error) {
		return c.Key, nil
	}

	var claims claims
	token, err := request.ParseFromRequestWithClaims(r,
		extractor{},
		&claims,
		keyFunc,
	)

	if err != nil || !token.Valid {
		return false, nil
	}

	u, err := c.Store.Users.Get(claims.User.ID, c.NewFS)
	if err != nil {
		return false, nil
	}

	c.User = u
	return true, u
}
