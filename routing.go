package main

import (
	"database/sql"
	"embed"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/google/uuid"

	"github.com/gin-contrib/multitemplate"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"

	"github.com/h2non/filetype"
)

//go:embed templates/*.html
var tfsys embed.FS

//go:embed static/*.css static/*.js
var sfsys embed.FS

func register(router *gin.Engine, db *sql.DB, data string, inlineTypes []string, chunkSize int64, timeout int) {
	// Initialization.

	pending := map[string]*time.Timer{}

	renderer := multitemplate.NewRenderer()

	renderer.Add("login", template.Must(template.ParseFS(tfsys, "templates/meta.html", "templates/login.html")))
	renderer.Add("files", template.Must(template.ParseFS(tfsys, "templates/meta.html", "templates/layout.html", "templates/files.html")))
	renderer.Add("file", template.Must(template.ParseFS(tfsys, "templates/meta.html", "templates/layout.html", "templates/file.html")))
	renderer.Add("unlock", template.Must(template.ParseFS(tfsys, "templates/meta.html", "templates/unlock.html")))

	router.HTMLRender = renderer

	subsfsys, _ := fs.Sub(sfsys, "static")
	router.StaticFS("/static", http.FS(subsfsys))

	priv := router.Group("/")

	priv.Use(func(ctx *gin.Context) {
		session := sessions.Default(ctx)

		uid := session.Get("user_id")

		// Reject guests.
		if uid == nil {
			ctx.Redirect(http.StatusFound, "/")
			ctx.Abort()
			return
		}

		// Verify that the user exists.
		row := db.QueryRow(`
			SELECT NULL
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

	// Utility functions.

	asUnit := func(unit string, d time.Duration) (time.Duration, error) {
		switch unit {
		case "days":
			return d * 24 * time.Hour, nil
		case "hours":
			return d * time.Hour, nil
		case "minutes":
			return d * time.Minute, nil
		case "seconds":
			return d * time.Second, nil
		default:
			return time.Duration(0), errors.New("invalid unit")
		}
	}

	offer := func(fileuuid string, filename string, ctx *gin.Context) {
		path := filepath.Join(data, fileuuid)
		ft, err := filetype.MatchFile(path)
		if err == nil {
			for _, it := range inlineTypes {
				if it == ft.MIME.Value {
					ctx.Writer.Header().Set("Content-Type", ft.MIME.Value)
					ctx.File(path)
					break
				}
			}
		}

		ctx.FileAttachment(filepath.Join(data, fileuuid), filename)
	}

	// Routes.

	router.GET("/", func(ctx *gin.Context) {
		session := sessions.Default(ctx)
		if session.Get("user_id") != nil {
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		ctx.HTML(http.StatusOK, "login", gin.H{})
	})

	router.POST("/login", func(ctx *gin.Context) {
		var in struct {
			Name     string `form:"name" binding:"required"`
			Password string `form:"password" binding:"required"`
		}
		err := ctx.ShouldBindWith(&in, binding.FormPost)
		if err != nil {
			log.Printf("Malformed input: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/")
			return
		}

		row := db.QueryRow(`
			SELECT id, password
			FROM user
			WHERE name = ?
		`, in.Name)

		var (
			userid   int
			password string
		)
		err = row.Scan(&userid, &password)
		if err != nil || bcrypt.CompareHashAndPassword([]byte(password), []byte(in.Password)) != nil {
			ctx.Redirect(http.StatusFound, "/")
			return
		}

		session := sessions.Default(ctx)
		session.Set("user_id", userid)
		err = session.Save()
		if err != nil {
			log.Printf("Could not save data to session: %s", err.Error())
			ctx.AbortWithStatus(500)
			return
		}

		ctx.Redirect(http.StatusFound, "/files/")
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
			AND done
		`, session.Get("user_id"))

		if err != nil {
			log.Printf("Could not query database: %s", err.Error())
			ctx.AbortWithStatus(500)
			return
		}
		defer func() {
			err := rows.Close()
			if err != nil {
				log.Printf("Unable to close rows: %s", err.Error())
				ctx.AbortWithStatus(500)
				return
			}
		}()

		var files []gin.H
		for rows.Next() {
			var (
				fileuuid string
				filename string
			)
			if err := rows.Scan(&fileuuid, &filename); err != nil {
				log.Printf("Could not copy values from database: %s", err.Error())
				ctx.AbortWithStatus(500)
				return
			}
			files = append(files, gin.H{
				"UUID": fileuuid,
				"Name": filename,
			})
		}
		if err = rows.Err(); err != nil {
			ctx.AbortWithStatus(500)
			return
		}

		ctx.HTML(http.StatusOK, "files", gin.H{
			"Files":     files,
			"ChunkSize": chunkSize,
		})
	})

	priv.POST("/upload", func(ctx *gin.Context) {
		session := sessions.Default(ctx)

		var in struct {
			Password string                `form:"password"`
			Time     int64                 `form:"time" binding:"required"`
			Unit     string                `form:"unit" binding:"required"`
			File     *multipart.FileHeader `form:"file" binding:"required"`
		}
		err := ctx.ShouldBindWith(&in, binding.FormMultipart)
		if err != nil {
			log.Printf("Malformed input: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		now := time.Now()
		expiry := now

		add, err := asUnit(in.Unit, time.Duration(in.Time))
		if err != nil {
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		expiry = expiry.Add(add)

		if expiry.After(now.Add(time.Duration(24*365) * time.Hour)) {
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		fileuuid := uuid.New().String()

		if err := ctx.SaveUploadedFile(in.File, filepath.Join(data, fileuuid)); err != nil {
			log.Printf("Unable to save uploaded file: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		var password sql.NullString
		if len(in.Password) == 0 {
			password = sql.NullString{}
		} else {
			hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
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
			INSERT INTO file (uuid, name, expiry, password, done, owner_id)
			VALUES (?, ?, ?, ?, 1, ?)
		`, fileuuid, in.File.Filename, expiry.Unix(), password, session.Get("user_id"))
		if err != nil {
			log.Printf("Unable to insert file: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		watch(fileuuid, expiry, data, db)

		ctx.Redirect(http.StatusFound, "/files/")
	})

	priv.POST("/prepare", func(ctx *gin.Context) {
		session := sessions.Default(ctx)

		var in struct {
			Password string `json:"password"`
			Time     int64  `json:"time" binding:"required"`
			Unit     string `json:"unit" binding:"required"`
			Filename string `json:"filename" binding:"required"`
		}
		err := ctx.ShouldBindJSON(&in)
		if err != nil {
			log.Printf("Malformed input: %s", err.Error())
			ctx.JSON(400, gin.H{
				"error": "Malformed input",
			})
			return
		}

		now := time.Now()
		expiry := now

		add, err := asUnit(in.Unit, time.Duration(in.Time))
		if err != nil {
			ctx.JSON(400, gin.H{
				"error": "Cannot convert duration to unit",
			})
			return
		}

		expiry = expiry.Add(add)

		if expiry.After(now.Add(time.Duration(24*365) * time.Hour)) {
			ctx.JSON(400, gin.H{
				"error": "Duration too long",
			})
			return
		}

		fileuuid := uuid.New().String()

		var password sql.NullString
		if len(in.Password) == 0 {
			password = sql.NullString{}
		} else {
			hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
			if err != nil {
				log.Printf("Unable to hash provided password: %s", err.Error())
				ctx.JSON(500, gin.H{
					"error": "Unable to hash provided password",
				})
				return
			}
			password = sql.NullString{
				String: string(hash),
				Valid:  true,
			}
		}

		_, err = db.Exec(`
			INSERT INTO file (uuid, name, expiry, password, done, owner_id)
			VALUES (?, ?, ?, ?, 0, ?)
		`, fileuuid, in.Filename, expiry.Unix(), password, session.Get("user_id"))
		if err != nil {
			log.Printf("Unable to insert file: %s", err.Error())
			ctx.JSON(500, gin.H{
				"error": "Unable to insert file",
			})
			return
		}

		ctx.JSON(http.StatusCreated, gin.H{
			"uuid": fileuuid,
		})

		pending[fileuuid] = time.AfterFunc(time.Duration(time.Second*time.Duration(timeout)), func() {
			log.Printf("File %s timed out", fileuuid)
			remove(fileuuid, data, db)
			delete(pending, fileuuid)
		})
	})

	priv.POST("/append/:uuid", func(ctx *gin.Context) {
		session := sessions.Default(ctx)

		var in struct {
			Chunk *multipart.FileHeader `form:"chunk" binding:"required"`
		}
		err := ctx.ShouldBindWith(&in, binding.FormMultipart)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{
				"error": "Malformed input",
			})
			return
		}

		if in.Chunk.Size > chunkSize {
			ctx.JSON(400, gin.H{
				"error": "Chunk too large",
			})
			return
		}

		fileuuid := ctx.Param("uuid")

		row := db.QueryRow(`
			SELECT NULL
			FROM file
			WHERE uuid = ?
			AND owner_id = ?
			AND NOT done
		`, fileuuid, session.Get("user_id"))
		if row.Err() != nil {
			ctx.JSON(400, gin.H{
				"error": "Metadata does not match",
			})
			return
		}

		pending[fileuuid].Stop()
		defer pending[fileuuid].Reset(time.Second * time.Duration(timeout))

		chunk, err := in.Chunk.Open()
		if err != nil {
			log.Printf("Unable to open chunk: %s", err.Error())
			ctx.JSON(500, gin.H{
				"error": "Unable to read chunk from form data",
			})
			return
		}
		defer func() {
			err = chunk.Close()
			if err != nil {
				log.Printf("Unable to close chunk file: %s", err.Error())
				ctx.JSON(500, gin.H{
					"error": "Unable to close chunk",
				})
				return
			}
		}()

		file, err := os.OpenFile(filepath.Join(data, ctx.Param("uuid")), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("Unable to open destination file: %s", err.Error())
			ctx.JSON(500, gin.H{
				"error": "Unable to open file",
			})
			return
		}
		defer func() {
			err = file.Close()
			if err != nil {
				log.Printf("Unable to close destination file: %s", err.Error())
				ctx.JSON(500, gin.H{
					"error": "Unable to close file",
				})
				return
			}
		}()

		// Append to the file identified by the UUID.
		_, err = io.Copy(file, chunk)
		if err != nil {
			log.Printf("Unable to append chunk to destination file: %s", err.Error())
			ctx.JSON(500, gin.H{
				"error": "Unable to append chunk",
			})
			return
		}
	})

	priv.POST("/finish/:uuid", func(ctx *gin.Context) {
		session := sessions.Default(ctx)

		fileuuid := ctx.Param("uuid")

		row := db.QueryRow(`
			SELECT NULL
			FROM file
			WHERE uuid = ?
			AND owner_id = ?
			AND NOT done
		`, fileuuid, session.Get("user_id"))
		if row.Err() != nil {
			ctx.JSON(400, gin.H{
				"error": "Metadata does not match",
			})
			return
		}

		pending[fileuuid].Stop()
		delete(pending, fileuuid)

		_, err := db.Exec(`
			UPDATE file
			SET done = 1
			WHERE uuid = ?
		`, ctx.Param("uuid"))
		if err != nil {
			log.Printf("Unable to mark file as done: %s", err.Error())
			ctx.JSON(500, gin.H{
				"error": "Could not mark file as done",
			})
			return
		}

		row = db.QueryRow(`
			SELECT expiry
			FROM file
			WHERE uuid = ?
			AND owner_id = ?
		`, ctx.Param("uuid"), session.Get("user_id"))

		var expiry int64
		if err := row.Scan(&expiry); err != nil {
			log.Printf("Could not get expiry from database: %s", err.Error())
			ctx.JSON(500, gin.H{
				"error": "Could not get expiry from database",
			})
			return
		}

		watch(ctx.Param("uuid"), time.Unix(expiry, 0), data, db)

		ctx.JSON(http.StatusOK, gin.H{})
	})

	priv.GET("/files/:uuid", func(ctx *gin.Context) {
		session := sessions.Default(ctx)

		row := db.QueryRow(`
			SELECT uuid, name, expiry
			FROM file
			WHERE uuid = ?
			AND owner_id = ?
			AND done
		`, ctx.Param("uuid"), session.Get("user_id"))

		var (
			fileuuid string
			filename string
			expiry   int64
		)
		if err := row.Scan(&fileuuid, &filename, &expiry); err != nil {
			log.Printf("Could not copy values from database: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		ctx.HTML(http.StatusOK, "file", gin.H{
			"File": gin.H{
				"UUID":   fileuuid,
				"Name":   filename,
				"Expiry": time.Unix(expiry, 0),
			},
		})
	})

	priv.POST("/revise", func(ctx *gin.Context) {
		session := sessions.Default(ctx)

		var in struct {
			UUID     string `form:"uuid" binding:"required"`
			Filename string `form:"filename" binding:"required"`
		}
		err := ctx.ShouldBindWith(&in, binding.FormPost)
		if err != nil {
			log.Printf("Malformed input: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/files/")
			return
		}

		_, err = db.Exec(`
			UPDATE file
			SET
				name = ?
			WHERE uuid = ?
			AND owner_id = ?
		`, in.Filename, in.UUID, session.Get("user_id"))
		if err != nil {
			log.Printf("Unable to update file: %s", err.Error())
		}

		ctx.Redirect(http.StatusFound, "/files/")
	})

	router.GET("/downloads/:uuid", func(ctx *gin.Context) {
		row := db.QueryRow(`
			SELECT f.uuid, f.name, f.password, u.id
			FROM file f
			JOIN user u
			ON f.owner_id = u.id
			WHERE f.uuid = ?
			AND done
		`, ctx.Param("uuid"))

		var (
			fileuuid string
			filename string
			password sql.NullString
			owner    int
		)
		if err := row.Scan(&fileuuid, &filename, &password, &owner); err != nil {
			log.Printf("Could not copy values from database: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/")
			return
		}

		session := sessions.Default(ctx)
		if session.Get("user_id") != owner && password.Valid {
			ctx.HTML(http.StatusOK, "unlock", gin.H{
				"File": gin.H{
					"UUID": fileuuid,
					"Name": filename,
				},
			})
		} else {
			offer(fileuuid, filename, ctx)
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
			AND done
		`, ctx.Param("uuid"))

		var fileuuid string
		var filename string
		var password sql.NullString
		var owner int
		if err := row.Scan(&fileuuid, &filename, &password, &owner); err != nil {
			log.Printf("Could not copy values from database: %s", err.Error())
			ctx.Redirect(http.StatusFound, "/")
			return
		}

		session := sessions.Default(ctx)
		if session.Get("user_id") != owner && password.Valid && bcrypt.CompareHashAndPassword([]byte(password.String), []byte(fpassword)) != nil {
			ctx.Redirect(http.StatusFound, "/")
			return
		}

		offer(fileuuid, filename, ctx)
	})
}
