package download

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"kapsel/internal/jobs"
)

func (d *Downloader) Handle(ctx context.Context, job jobs.Job) error {
	if err := d.requireJobStoreForJob(job); err != nil {
		return err
	}
	_, err := d.handlePayload(ctx, job.ID, job.PayloadJSON)

	return err
}

func (d *Downloader) jobStore() (*jobs.Store, error) {
	if d.store == nil {
		return nil, errors.New("download handler missing job store")
	}

	return d.store, nil
}

func (d *Downloader) requireJobStoreForJob(job jobs.Job) error {
	if job.ID == "" {
		return nil
	}
	_, err := d.jobStore()

	return err
}

func (d *Downloader) setJobResult(ctx context.Context, jobID string, result any) error {
	if jobID == "" {
		return nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	store, err := d.jobStore()
	if err != nil {
		return err
	}

	return store.CompleteWithResult(ctx, jobID, string(body))
}

func (d *Downloader) setPartialJobResult(ctx context.Context, jobID string, result any) error {
	if jobID == "" {
		return nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	store, err := d.jobStore()
	if err != nil {
		return err
	}

	return store.SetPartialResult(ctx, jobID, string(body))
}

func (d *Downloader) setJobResultTx(ctx context.Context, tx *sql.Tx, jobID string, result any) error {
	if jobID == "" {
		return nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	store, err := d.jobStore()
	if err != nil {
		return err
	}

	return store.CompleteWithResultTx(ctx, tx, jobID, string(body))
}

func (d *Downloader) finishChannelJob(ctx context.Context, jobID string, channelID string, subscribe bool, result any) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if channelID != "" {
		if subscribe {
			if err := d.markChannelSubscribed(ctx, tx, channelID); err != nil {
				return err
			}
		}
		if err := d.markChannelScanned(ctx, tx, channelID); err != nil {
			return err
		}
	}
	if err := d.setJobResultTx(ctx, tx, jobID, result); err != nil {
		return err
	}

	return tx.Commit()
}

func (d *Downloader) finishChannelFirstJob(ctx context.Context, jobID string, catalogResult channelCatalogResult, firstVideoURL string) error {
	store, err := d.jobStore()
	if err != nil {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if catalogResult.ChannelID != "" {
		if err := d.markChannelSubscribed(ctx, tx, catalogResult.ChannelID); err != nil {
			return err
		}
		if err := d.markChannelScanned(ctx, tx, catalogResult.ChannelID); err != nil {
			return err
		}
	}
	downloadJob, _, err := enqueueDownloadTx(ctx, store, tx, Payload{URL: firstVideoURL}, true)
	if err != nil {
		return err
	}
	result := channelFirstResult{
		ChannelID:     catalogResult.ChannelID,
		Videos:        catalogResult.Videos,
		FirstVideoURL: firstVideoURL,
		DownloadJobID: downloadJob.ID,
		Catalog:       catalogResult,
	}
	if err := d.setJobResultTx(ctx, tx, jobID, result); err != nil {
		return err
	}

	return tx.Commit()
}

func (d *Downloader) finishChannelFirstScanOnlyJob(ctx context.Context, jobID string, catalogResult channelCatalogResult) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if catalogResult.ChannelID != "" {
		if err := d.markChannelSubscribed(ctx, tx, catalogResult.ChannelID); err != nil {
			return err
		}
		if err := d.markChannelScanned(ctx, tx, catalogResult.ChannelID); err != nil {
			return err
		}
	}
	result := channelFirstResult{
		ChannelID: catalogResult.ChannelID,
		Videos:    catalogResult.Videos,
		Catalog:   catalogResult,
	}
	if err := d.setJobResultTx(ctx, tx, jobID, result); err != nil {
		return err
	}

	return tx.Commit()
}
