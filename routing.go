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

	"github.com/lukaswrz/hiraeth/config"
)

func register(router *gin.Engine, c config.Config, db *sql.DB) {
	renderer := multitemplate.NewRenderer()
	renderer.AddFromFiles("login", "templates/meta.html", "templates/login.html")
	renderer.AddFromFiles("files", "templates/meta.html", "templates/layout.html", "templates/files.html")
	renderer.AddFromFiles("file", "templates/meta.html", "templates/layout.html", "templates/file.html")

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

	pub := router.Group("/")

	pub.GET("/style.css", func(ctx *gin.Context) {
		ctx.File("static/style.css")
	})

	pub.GET("/", func(ctx *gin.Context) {
		session := sessions.Default(ctx)
		if session.Get("user_id") != nil {
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		ctx.HTML(http.StatusOK, "login", gin.H{})
	})

	pub.POST("/login", func(ctx *gin.Context) {
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

		expiry := time.Now()

		var param int
		param, err := strconv.Atoi(ctx.Request.PostFormValue("hours"))
		if err != nil || param > 24 || param < 0 {
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}
		expiry = expiry.Add(time.Duration(param) * time.Hour)
		param, err = strconv.Atoi(ctx.PostForm("minutes"))
		if err != nil || param > 59 || param < 0 {
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}
		expiry = expiry.Add(time.Duration(param) * time.Minute)
		param, err = strconv.Atoi(ctx.PostForm("seconds"))
		if err != nil || param > 59 || param < 0 {
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}
		expiry = expiry.Add(time.Duration(param) * time.Second)

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

		_, err = db.Exec(`
			INSERT INTO file (uuid, name, expiry, owner_id)
			VALUES (?, ?, ?, ?)
		`, file.UUID, file.Name, file.Expiry.Unix(), session.Get("user_id"))
		if err != nil {
			log.Printf("Unable to insert file: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		watch(file, c, db)

		ctx.Redirect(http.StatusFound, "/files/")
	})

	priv.GET("/files/:uuid/", func(ctx *gin.Context) {
		session := sessions.Default(ctx)

		var file file

		row := db.QueryRow(`
			SELECT uuid, name, expiry
			FROM file
			WHERE uuid = ?
			AND owner_id = ?
		`, ctx.Param("uuid"), session.Get("user_id"))

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

	priv.GET("/files/:uuid/:name", func(ctx *gin.Context) {
		ctx.AbortWithStatus(404)
		return
	})
}
