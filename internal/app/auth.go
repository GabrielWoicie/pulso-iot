package app

import (
	"context"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

const userCols = "id,name,email,role,status,last_login_at,created_at,updated_at"

type userContext struct{ ID, Role string }
type userKey struct{}

func actor(r *http.Request) string { return r.Context().Value(userKey{}).(userContext).ID }
func allowed(role, permission string) bool {
	switch permission {
	case "read":
		return role == "admin" || role == "operator" || role == "viewer"
	case "write":
		return role == "admin" || role == "operator"
	case "admin":
		return role == "admin"
	}
	return false
}
func (a *App) authenticate(r *http.Request, permission string) (*http.Request, error) {
	s := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims := &jwt.RegisteredClaims{}
	t, e := jwt.ParseWithClaims(s, claims, func(t *jwt.Token) (any, error) { return []byte(a.C.JWTSecret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuer("pulso-iot"), jwt.WithAudience("pulso-admin"))
	if e != nil || !t.Valid {
		return r, problem(401, "UNAUTHORIZED", "Sessão inválida ou expirada")
	}
	if _, e = uuid.Parse(claims.Subject); e != nil {
		return r, problem(401, "UNAUTHORIZED", "Sessão inválida")
	}
	var u userContext
	var status string
	e = a.DB.QueryRow(r.Context(), "SELECT id::text,role,status FROM pulso_iot.users WHERE id=$1", claims.Subject).Scan(&u.ID, &u.Role, &status)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return r, e
	}
	if e != nil || status != "active" {
		return r, problem(401, "UNAUTHORIZED", "Usuário indisponível")
	}
	if !allowed(u.Role, permission) {
		return r, problem(403, "FORBIDDEN", "Permissão insuficiente")
	}
	return r.WithContext(context.WithValue(r.Context(), userKey{}, u)), nil
}
func (a *App) rateLimit(key string) bool {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	now := time.Now()
	for k, v := range a.attempts {
		if now.After(v.Until) {
			delete(a.attempts, k)
		}
	}
	if len(a.attempts) >= 10000 {
		return false
	}
	v := a.attempts[key]
	if v.Until.IsZero() {
		v.Until = now.Add(10 * time.Minute)
	}
	v.Count++
	a.attempts[key] = v
	return v.Count <= 10
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-login-not-a-user-!123"), bcrypt.DefaultCost)

func (a *App) login(w http.ResponseWriter, r *http.Request) error {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !a.rateLimit(ip) {
		return problem(429, "RATE_LIMITED", "Muitas tentativas; aguarde 10 minutos")
	}
	var b struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	var id, hash, status string
	e := a.DB.QueryRow(r.Context(), "SELECT id::text,password_hash,status FROM pulso_iot.users WHERE lower(email)=lower($1)", b.Email).Scan(&id, &hash, &status)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if e != nil {
		hash = string(dummyHash)
	}
	valid := bcrypt.CompareHashAndPassword([]byte(hash), []byte(b.Password)) == nil
	if !valid || e != nil || status != "active" {
		if id != "" {
			if e = audit(r.Context(), a.DB, id, "auth.login_failed", "users", id); e != nil {
				return e
			}
		}
		return problem(401, "INVALID_CREDENTIALS", "E-mail ou senha inválidos")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), "UPDATE pulso_iot.users SET last_login_at=now() WHERE id=$1", id); e != nil {
		return e
	}
	if e = audit(r.Context(), tx, id, "auth.login", "users", id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{Subject: id, Issuer: "pulso-iot", Audience: []string{"pulso-admin"}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(a.C.TokenTTL)), ID: uuid.NewString()}
	token, e := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(a.C.JWTSecret))
	if e != nil {
		return e
	}
	data(w, 200, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": int(a.C.TokenTTL.Seconds())})
	return nil
}
func (a *App) me(w http.ResponseWriter, r *http.Request) error {
	v, e := oneJSON(r.Context(), a.DB, "SELECT "+userCols+" FROM pulso_iot.users WHERE id=$1", actor(r))
	if e == nil {
		data(w, 200, v)
	}
	return e
}
func emailOK(s string) bool {
	v, e := mail.ParseAddress(s)
	return e == nil && v.Address == s && len(s) <= 255
}
func (a *App) users(w http.ResponseWriter, r *http.Request) error {
	q, args := filters(r, map[string]string{"role": "role", "status": "status"})
	if s := r.URL.Query().Get("search"); s != "" {
		p := arg(&args, "%"+s+"%")
		q += " AND (name ILIKE " + p + " OR email ILIKE " + p + ")"
	}
	return a.list(w, r, "SELECT "+userCols+" FROM pulso_iot.users", q, "created_at DESC,id", args)
}
func (a *App) user(w http.ResponseWriter, r *http.Request) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	v, e := oneJSON(r.Context(), a.DB, "SELECT "+userCols+" FROM pulso_iot.users WHERE id=$1", id)
	if e == nil {
		data(w, 200, v)
	}
	return e
}
func (a *App) createUser(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
		Status   string `json:"status"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	b.Name = strings.TrimSpace(b.Name)
	b.Email = strings.ToLower(strings.TrimSpace(b.Email))
	if b.Status == "" {
		b.Status = "active"
	}
	if b.Role == "" {
		b.Role = "viewer"
	}
	if len(b.Name) < 1 || len(b.Name) > 150 || !emailOK(b.Email) || !validPassword(b.Password) || !allowed(b.Role, "read") || (b.Status != "active" && b.Status != "inactive") {
		return problem(422, "VALIDATION_ERROR", "Nome, e-mail, senha (12–72 bytes com letras, números e símbolo), papel ou status inválido")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(b.Password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	var id string
	e = tx.QueryRow(r.Context(), `INSERT INTO pulso_iot.users(name,email,password_hash,role,status) VALUES($1,$2,$3,$4,$5) RETURNING id::text`, b.Name, b.Email, string(hash), b.Role, b.Status).Scan(&id)
	if e != nil {
		return e
	}
	if e = audit(r.Context(), tx, actor(r), "user.created", "users", id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	v, e := oneJSON(r.Context(), a.DB, "SELECT "+userCols+" FROM pulso_iot.users WHERE id=$1", id)
	if e == nil {
		data(w, 201, v)
	}
	return e
}
func (a *App) updateUser(w http.ResponseWriter, r *http.Request) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	var b struct {
		Name   *string `json:"name"`
		Email  *string `json:"email"`
		Role   *string `json:"role"`
		Status *string `json:"status"`
	}
	if r.Method == "DELETE" {
		s := "inactive"
		b.Status = &s
	} else if e = decode(w, r, &b); e != nil {
		return e
	}
	if b.Name != nil && (strings.TrimSpace(*b.Name) == "" || len(*b.Name) > 150) || b.Email != nil && !emailOK(*b.Email) || b.Role != nil && !allowed(*b.Role, "read") || b.Status != nil && *b.Status != "active" && *b.Status != "inactive" {
		return problem(422, "VALIDATION_ERROR", "Campos inválidos")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(741001)"); e != nil {
		return e
	}
	var role, status string
	if e = tx.QueryRow(r.Context(), "SELECT role,status FROM pulso_iot.users WHERE id=$1 FOR UPDATE", id).Scan(&role, &status); e != nil {
		return e
	}
	if role == "admin" && status == "active" && (b.Role != nil && *b.Role != "admin" || b.Status != nil && *b.Status != "active") {
		var n int
		if e = tx.QueryRow(r.Context(), "SELECT count(*) FROM pulso_iot.users WHERE role='admin' AND status='active'").Scan(&n); e != nil {
			return e
		}
		if n <= 1 {
			return problem(409, "LAST_ADMIN", "Preserve ao menos um administrador ativo")
		}
	}
	_, e = tx.Exec(r.Context(), `UPDATE pulso_iot.users SET name=COALESCE($2,name),email=lower(COALESCE($3,email)),role=COALESCE($4,role),status=COALESCE($5,status) WHERE id=$1`, id, b.Name, b.Email, b.Role, b.Status)
	if e != nil {
		return e
	}
	if e = audit(r.Context(), tx, actor(r), "user.updated", "users", id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	return a.user(w, r)
}
func (a *App) resetPassword(w http.ResponseWriter, r *http.Request) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	var b struct {
		Password string `json:"password"`
	}
	if e = decode(w, r, &b); e != nil {
		return e
	}
	if !validPassword(b.Password) {
		return problem(422, "WEAK_PASSWORD", "Senha exige 12–72 bytes, letras, números e símbolo")
	}
	h, e := bcrypt.GenerateFromPassword([]byte(b.Password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	tag, e := tx.Exec(r.Context(), "UPDATE pulso_iot.users SET password_hash=$2 WHERE id=$1", id, string(h))
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if e = audit(r.Context(), tx, actor(r), "user.password_reset", "users", id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	data(w, 200, map[string]bool{"updated": true})
	return nil
}

// Bootstrap is serialized with admin edits and refuses to create a second first admin.
func Bootstrap(ctx context.Context, db DB, name, email, password string) error {
	if strings.TrimSpace(name) == "" || len(name) > 150 || !emailOK(email) || !validPassword(password) {
		return errors.New("bootstrap: nome/e-mail/senha inválidos")
	}
	var n int
	if e := db.QueryRow(ctx, "SELECT count(*) FROM pulso_iot.users WHERE role='admin' AND status='active'").Scan(&n); e != nil {
		return e
	}
	if n != 0 {
		return errors.New("administrador ativo já existe")
	}
	h, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	var id string
	if e = db.QueryRow(ctx, "INSERT INTO pulso_iot.users(name,email,password_hash,role) VALUES($1,lower($2),$3,'admin') RETURNING id::text", name, email, string(h)).Scan(&id); e != nil {
		return e
	}
	return audit(ctx, db, "", "user.bootstrap", "users", id)
}
