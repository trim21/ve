package client

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
	"github.com/sourcegraph/conc"
	"github.com/sourcegraph/conc/panics"
)

func (c *Client) Shutdown() {
	log.Info().Msg("core shutting down...")

	c.m.Lock()
	defer c.m.Unlock()

	c.saveSession()

	c.cancel()
}

func (c *Client) saveSession() *panics.Recovered {
	var w = conc.NewWaitGroup()

	for _, d := range c.downloads {
		w.Go(func() {
			d.m.Lock()
			defer d.m.Unlock()

			b, err := d.MarshalBinary()
			if err != nil {
				log.Err(err).Msg("failed to save download")
				return
			}

			err = os.WriteFile(filepath.Join(c.sessionPath, "torrents", fmt.Sprintf("%x.resume", d.hash)), b, os.ModePerm)
			if err != nil {
				log.Err(err).Msg("failed to save download")
			}
		})
	}

	return w.WaitAndRecover()
}
