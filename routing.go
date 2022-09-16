package main

import (
	"database/sql"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/google/uuid"

	"github.com/gin-contrib/multitemplate"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/redis"
	"github.com/gin-gonic/gin"

	"github.com/h2non/filetype"

	"github.com/lukaswrz/hiraeth/config"
)

func download(file file, ctx *gin.Context, c config.Config) {
	path := filepath.Join(c.Data, file.UUID)
	ft, err := filetype.MatchFile(path)
	if err == nil {
		for _, it := range c.InlineTypes {
			if it == ft.MIME.Value {
				ctx.Writer.Header().Set("Content-Type", ft.MIME.Value)
				ctx.File(path)
				break
			}
		}
	}

	ctx.FileAttachment(filepath.Join(c.Data, file.UUID), file.Name)
}

func register(router *gin.Engine, c config.Config, db *sql.DB) {
	renderer := multitemplate.NewRenderer()
	renderer.AddFromFiles("login", "templates/meta.html", "templates/login.html")
	renderer.AddFromFiles("files", "templates/meta.html", "templates/layout.html", "templates/files.html")
	renderer.AddFromFiles("file", "templates/meta.html", "templates/layout.html", "templates/file.html")
	renderer.AddFromFiles("unlock", "templates/meta.html", "templates/unlock.html")

	router.HTMLRender = renderer

	kp := [][]byte{}
	for _, value := range c.Redis.KeyPairs {
		kp = append(kp, []byte(value))
	}
	store, err := redis.NewStore(c.Redis.Connections, c.Redis.Network, c.Redis.Address, c.Redis.Password, kp...)
	if err != nil {
		log.Fatalf("Unable to create Redis store: %s", err.Error())
	}

	router.Use(sessions.Sessions("session", store))

	router.GET("/style.css", func(ctx *gin.Context) {
		ctx.File("static/style.css")
	})

	router.GET("/", func(ctx *gin.Context) {
		session := sessions.Default(ctx)
		if session.Get("user_id") != nil {
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		ctx.HTML(http.StatusOK, "login", gin.H{})
	})

	router.POST("/login", func(ctx *gin.Context) {
		fuser := user{
			Name:     ctx.PostForm("name"),
			Password: ctx.PostForm("password"),
		}

		row := db.QueryRow(`
			SELECT id, password
			FROM user
			WHERE name = ?
		`, fuser.Name)

		var user user
		err := row.Scan(&user.ID, &user.Password)
		if err != nil || bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(fuser.Password)) != nil {
			ctx.Redirect(http.StatusFound, "/")
			return
		}

		session := sessions.Default(ctx)
		session.Set("user_id", user.ID)
		err = session.Save()
		if err != nil {
			log.Printf("Could not save data to session: %s", err.Error())
			ctx.AbortWithStatus(500)
			return
		}

		ctx.Redirect(http.StatusFound, "/files/")
	})

	priv := router.Group("/")

	// Reject guests.
	priv.Use(func(ctx *gin.Context) {
		session := sessions.Default(ctx)

		uid := session.Get("user_id")

		if uid == nil {
			ctx.Redirect(http.StatusFound, "/")
			ctx.Abort()
			return
		}

		// Verify that the user exists.
		row := db.QueryRow(`
			SELECT 1
			FROM user
			WHERE id = ?
		`, uid)

		if row.Err() != nil {
			ctx.Redirect(http.StatusFound, "/")
			ctx.Abort()
			return
		}

		ctx.Next()
	})

	priv.POST("/logout", func(ctx *gin.Context) {
		session := sessions.Default(ctx)
		session.Clear()
		session.Save()

		ctx.Redirect(http.StatusFound, "/")
	})

	priv.GET("/files/", func(ctx *gin.Context) {
		session := sessions.Default(ctx)

		rows, err := db.Query(`
			SELECT uuid, name
			FROM file
			WHERE owner_id = ?
		`, session.Get("user_id"))

		if err != nil {
			log.Printf("Could not query database: %s", err.Error())
			ctx.AbortWithStatus(500)
			return
		}
		defer rows.Close()

		var files []file

		for rows.Next() {
			var file file
			if err := rows.Scan(&file.UUID, &file.Name); err != nil {
				log.Printf("Could not copy values from database: %s", err.Error())
				ctx.AbortWithStatus(500)
				return
			}
			files = append(files, file)
		}
		if err = rows.Err(); err != nil {
			ctx.AbortWithStatus(500)
			return
		}

		ctx.HTML(http.StatusOK, "files", gin.H{
			"Files": files,
		})
	})

	priv.POST("/upload", func(ctx *gin.Context) {
		session := sessions.Default(ctx)

		fpassword := ctx.PostForm("password")

		now := time.Now()
		expiry := now

		param, err := strconv.Atoi(ctx.PostForm("time"))
		if err != nil {
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		var duration time.Duration
		switch ctx.PostForm("unit") {
		case "days":
			duration = time.Duration(param*24) * time.Hour
		case "hours":
			duration = time.Duration(param) * time.Hour
		case "minutes":
			duration = time.Duration(param) * time.Minute
		case "seconds":
			duration = time.Duration(param) * time.Second
		default:
			ctx.AbortWithStatus(http.StatusBadRequest)
			return
		}
		expiry = expiry.Add(duration)

		if expiry.After(now.Add(time.Duration(24*365) * time.Hour)) {
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		ffile, err := ctx.FormFile("file")
		if err != nil {
			log.Printf("Unable to read file from form data: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		file := file{
			UUID:   uuid.New().String(),
			Name:   ffile.Filename,
			Expiry: expiry,
		}

		if err := ctx.SaveUploadedFile(ffile, filepath.Join(c.Data, file.UUID)); err != nil {
			log.Printf("Unable to save uploaded file: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		var password sql.NullString
		if len(fpassword) == 0 {
			password = sql.NullString{}
		} else {
			hash, err := bcrypt.GenerateFromPassword([]byte(fpassword), 12)
			if err != nil {
				log.Printf("Unable to hash provided password: %s", err.Error())
				ctx.Redirect(http.StatusFound, "/files/")
				return
			}
			password = sql.NullString{
				String: string(hash),
				Valid:  true,
			}
		}

		_, err = db.Exec(`
			INSERT INTO file (uuid, name, expiry, password, owner_id)
			VALUES (?, ?, ?, ?, ?)
		`, file.UUID, file.Name, file.Expiry.Unix(), password, session.Get("user_id"))
		if err != nil {
			log.Printf("Unable to insert file: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		watch(file, c, db)

		ctx.Redirect(http.StatusFound, "/files/")
	})

	priv.GET("/files/:uuid", func(ctx *gin.Context) {
		session := sessions.Default(ctx)

		row := db.QueryRow(`
			SELECT uuid, name, expiry
			FROM file
			WHERE uuid = ?
			AND owner_id = ?
		`, ctx.Param("uuid"), session.Get("user_id"))

		var file file
		var expiry int64
		if err := row.Scan(&file.UUID, &file.Name, &expiry); err != nil {
			log.Printf("Could not copy values from database: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}
		file.Expiry = time.Unix(expiry, 0)

		ctx.HTML(http.StatusOK, "file", gin.H{
			"File": file,
		})
	})

	priv.POST("/revise", func(ctx *gin.Context) {
		session := sessions.Default(ctx)

		file := file{
			UUID: ctx.PostForm("uuid"),
			Name: ctx.PostForm("name"),
		}

		_, err = db.Exec(`
			UPDATE file
			SET
				name = ?
			WHERE uuid = ?
			AND owner_id = ?
		`, file.Name, file.UUID, session.Get("user_id"))
		if err != nil {
			log.Printf("Unable to update file: %s", err.Error())
		}

		ctx.Redirect(http.StatusFound, "/files/")
		return
	})

	router.GET("/downloads/:uuid", func(ctx *gin.Context) {
		row := db.QueryRow(`
			SELECT f.uuid, f.name, f.password, u.id, u.name
			FROM file f
			JOIN user u
			ON f.owner_id = u.id
			WHERE f.uuid = ?
		`, ctx.Param("uuid"))

		var file file
		var password sql.NullString
		var owner user
		if err := row.Scan(&file.UUID, &file.Name, &password, &owner.ID, &owner.Name); err != nil {
			log.Printf("Could not copy values from database: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/")
			return
		}

		session := sessions.Default(ctx)
		if session.Get("user_id") != owner.ID && password.Valid {
			ctx.HTML(http.StatusOK, "unlock", gin.H{
				"File": file,
			})
		} else {
			download(file, ctx, c)
		}
	})

	router.POST("/downloads/:uuid", func(ctx *gin.Context) {
		fpassword := ctx.PostForm("password")

		row := db.QueryRow(`
			SELECT f.uuid, f.name, f.password, u.id, u.name
			FROM file f
			JOIN user u
			ON f.owner_id = u.id
			WHERE f.uuid = ?
		`, ctx.Param("uuid"))

		var file file
		var password sql.NullString
		var owner user
		if err := row.Scan(&file.UUID, &file.Name, &password, &owner.ID, &owner.Name); err != nil {
			log.Printf("Could not copy values from database: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/")
			return
		}

		session := sessions.Default(ctx)
		if session.Get("user_id") != owner.ID && password.Valid && bcrypt.CompareHashAndPassword([]byte(password.String), []byte(fpassword)) != nil {
			ctx.Redirect(http.StatusFound, "/")
			return
		}

		download(file, ctx, c)
	})
}
