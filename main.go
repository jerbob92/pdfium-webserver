package main

import (
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/multi_threaded"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/ory/graceful"

	logrus_logstash "github.com/bshuster-repo/logrus-logstash-hook"
	"github.com/penglongli/gin-metrics/ginmetrics"
	log "github.com/sirupsen/logrus"
	"github.com/toorop/gin-logrus"
)

// Be sure to close pools/instances when you're done with them.
var pool pdfium.Pool

func initPdfium() {
	if os.Getenv("ENVIRONMENT") == "production" {
		// Fake a long pdfium startup time.
		time.Sleep(time.Second * 60)
	}

	cmd := multi_threaded.Command{
		BinPath: "go",                              // Only do this while developing, on production put the actual binary path in here. You should not want the Go runtime on production.
		Args:    []string{"run", "worker/main.go"}, // This is a reference to the worker package, this can be left empty when using a direct binary path.
	}

	if os.Getenv("PDFIUM_WORKER") != "" {
		cmd = multi_threaded.Command{
			BinPath: os.Getenv("PDFIUM_WORKER"),
		}
	}

	// Init the PDFium library and return the instance to open documents.
	// You can tweak these configs to your need. Be aware that workers can use quite some memory.
	pool = multi_threaded.Init(multi_threaded.Config{
		MinIdle:  4, // Makes sure that at least x workers are always available
		MaxIdle:  4, // Makes sure that at most x workers are ever available
		MaxTotal: 4, // Maxium amount of workers in total, allows the amount of workers to grow when needed, items between total max and idle max are automatically cleaned up, while idle workers are kept alive so they can be used directly.
		Command:  cmd,
	})

}

type BindFile struct {
	DPI  int                   `form:"dpi" binding:"required"`
	Page int                   `form:"page" binding:"required"`
	File *multipart.FileHeader `form:"file" binding:"required"`
}

func main() {
	go initPdfium()

	if os.Getenv("ENVIRONMENT") == "production" {
		gin.SetMode(gin.ReleaseMode)
		log.SetFormatter(logrus_logstash.DefaultFormatter(log.Fields{
			"type": "Pdfium API",
		}))

	}

	r := gin.New()
	r.Use(ginlogrus.Logger(log.StandardLogger()), gin.Recovery())

	m := ginmetrics.GetMonitor()

	m.SetMetricPath("/metrics")
	m.SetSlowTime(1)
	m.SetDuration([]float64{0.1, 0.3, 1.2, 5, 10})

	m.Use(r)

	// The /readyz endpoint can be used by kubernetes as a readiness endpoint.
	// It gives an indication whether the API is ready to use.
	r.GET("/readyz", func(c *gin.Context) {
		status := 200
		result := gin.H{
			"pdfium_ready": true,
		}

		if pool == nil {
			status = 500
			result["pdfium_ready"] = false
		}

		c.JSON(status, result)
	})

	// The /livez endpoint can be used by kubernetes as liveness endpoint. When
	// this endpoint cannot be reached the container will be restarted. This
	// should only be done when the API is not responding at all.
	r.GET("/livez", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status": "ok",
		})
	})

	r.POST("/render", func(c *gin.Context) {
		var bindFile BindFile

		if err := c.ShouldBind(&bindFile); err != nil {
			c.String(http.StatusBadRequest, fmt.Sprintf("err: %s", err.Error()))
			return
		}

		src, err := bindFile.File.Open()
		if err != nil {
			c.String(http.StatusBadRequest, fmt.Sprintf("upload file err: %s", err.Error()))
			return
		}
		defer src.Close()

		pdfiumInstance, err := pool.GetInstance(time.Second * 30)
		if err != nil {
			c.String(http.StatusBadRequest, fmt.Sprintf("pdfium err: %s", err.Error()))
			return
		}
		defer pdfiumInstance.Close()

		doc, err := pdfiumInstance.OpenDocument(&requests.OpenDocument{
			FileReader: src,
		})
		if err != nil {
			c.String(http.StatusBadRequest, fmt.Sprintf("pdfium err: %s", err.Error()))
			return
		}

		page := bindFile.Page
		if page <= 0 {
			page = 1
		}

		renderedPages, err := pdfiumInstance.RenderToFile(&requests.RenderToFile{
			RenderPagesInDPI: &requests.RenderPagesInDPI{
				Pages: []requests.RenderPageInDPI{
					{
						Page: requests.Page{
							ByIndex: &requests.PageByIndex{
								Document: doc.Document,
								Index:    page - 1,
							},
						},
						DPI: bindFile.DPI,
					},
				},
			},
			OutputFormat: requests.RenderToFileOutputFormatJPG,
			OutputTarget: requests.RenderToFileOutputTargetBytes,
		})
		if err != nil {
			c.String(http.StatusBadRequest, fmt.Sprintf("pdfium err: %s", err.Error()))
			return
		}

		c.Data(200, "image/jpeg", *renderedPages.ImageBytes)
	})

	server := &http.Server{Addr: ":8082", Handler: r.Handler()}

	log.Printf("Starting API on address %s", server.Addr)

	// We use a large shutdown timeout here because requests to our API
	// potentially take a very long time, and we don't want requests to fail
	// just because we deploy a new version. This waits 3 minutes, which is
	// the timeout of most parsers + some extra time for other processing.
	graceful.DefaultShutdownTimeout = time.Minute
	if err := graceful.Graceful(server.ListenAndServe, server.Shutdown); err != nil {
		log.Fatal(fmt.Errorf("could not start webserver: %w", err))
	}

	err := server.ListenAndServe()
	if err != http.ErrServerClosed {
		log.Fatal(fmt.Errorf("could not start webserver on address %s: %w", server.Addr, err))
	}
}
