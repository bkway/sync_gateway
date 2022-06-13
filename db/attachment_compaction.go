package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	sgbucket "github.com/couchbase/sg-bucket"
	"github.com/couchbase/sync_gateway/base"
	"github.com/couchbase/sync_gateway/channels"
	"github.com/couchbase/sync_gateway/logger"
)

const CompactionIDKey = "compactID"

func attachmentCompactMarkPhase(db *Database, compactionID string, terminator *base.SafeTerminator, markedAttachmentCount *base.AtomicInt) (count int64, vbUUIDs []uint64, err error) {
	//	logger.InfofCtx(db.Ctx, logger.KeyAll, "Starting first phase of attachment compaction (mark phase) with compactionID: %q", compactionID)
	logger.For(logger.UnknownKey).Info().Msgf("Starting first phase of attachment compaction (mark phase) with compactionID: %q", compactionID)
	compactionLoggingID := "Compaction Mark: " + compactionID

	var markProcessFailureErr error

	// failProcess used when a failure is deemed as 'un-recoverable' and we need to abort the compaction process.
	failProcess := func(err error, format string, args ...interface{}) bool {
		markProcessFailureErr = err
		terminator.Close()
		logger.For(logger.UnknownKey).Warn().Err(err).Msgf(format, args...)
		return false
	}

	callback := func(event sgbucket.FeedEvent) bool {
		docID := string(event.Key)

		// We've had an error previously so no point doing work for any remaining items
		if markProcessFailureErr != nil {
			return false
		}

		// Don't want to process raw binary docs
		// The binary check should suffice but for additional safety also check for empty bodies
		if event.DataType == base.MemcachedDataTypeRaw || len(event.Value) == 0 {
			return true
		}

		// We only want to process full docs. Not any sync docs.
		if strings.HasPrefix(docID, base.SyncPrefix) {
			return true
		}

		// We need to mark attachments in every leaf revision of the current doc
		// We will build up a list of attachment names which map to attachment doc IDs. Avoids doing multiple KV ops
		// when marking if multiple leaves are referencing the same attachment.
		attachmentKeys := make(map[string]string)
		attachmentData, err := getAttachmentSyncData(event.DataType, event.Value)
		if err != nil {
			return failProcess(err, "[%s] Failed to obtain required sync data from doc %s from feed. Err: %v", compactionID, logger.UD(docID), err)
		}

		// Its possible a doc doesn't have sync data. If not a sync gateway doc we can skip it.
		if attachmentData == nil {
			return true
		}

		handleAttachments(attachmentKeys, docID, attachmentData.Attachments)

		// If we're in a conflict state we need to go and check and mark attachments from other leaves, not just winning
		if attachmentData.Flags&channels.Conflict != 0 {
			// Iterate over body map
			// These are strings containing conflicting bodies, need to scan these for attachments
			for _, bodyMap := range attachmentData.History.BodyMap {
				var body AttachmentsMetaMap
				err = json.Unmarshal([]byte(bodyMap), &body)
				if err != nil {
					continue
				}

				handleAttachments(attachmentKeys, docID, body.Attachments)
			}

			// Iterate over body key map
			// These are strings containing IDs to documents containing conflicting bodies
			for _, bodyKey := range attachmentData.History.BodyKeyMap {
				bodyRaw, _, err := db.Bucket.GetRaw(bodyKey)
				if err != nil {
					if base.IsDocNotFoundError(err) {
						continue
					}
					return failProcess(err, "[%s] Unable to obtain document from %s bodyKeyMap with ID %s: %v", compactionID, logger.UD(docID), logger.UD(bodyKey), err)
				}

				var body AttachmentsMetaMap
				err = json.Unmarshal(bodyRaw, &body)
				if err != nil {
					continue
				}

				handleAttachments(attachmentKeys, docID, body.Attachments)
			}
		}

		for attachmentName, attachmentDocID := range attachmentKeys {
			// Stamp the current compaction ID into the attachment xattr. This is performing the actual marking
			_, err = db.Bucket.SetXattr(attachmentDocID, getCompactionIDSubDocPath(compactionID), []byte(strconv.Itoa(int(time.Now().Unix()))))

			// If an error occurs while stamping in that ID we need to fail this process and then the entire compaction
			// process. Otherwise, an attachment could end up getting erroneously deleted in the later sweep phase.
			if err != nil {
				return failProcess(err, "[%s] Failed to mark attachment %s from doc %s with attachment docID %s. Err: %v", compactionLoggingID, logger.UD(attachmentName), logger.UD(docID), logger.UD(attachmentDocID), err)
			}

			//			logger.DebugfCtx(db.Ctx, logger.KeyAll, "[%s] Marked attachment %s from doc %s with attachment docID %s", compactionLoggingID, logger.UD(attachmentName), logger.UD(docID), logger.UD(attachmentDocID))
			logger.For(logger.UnknownKey).Debug().Msgf("[%s] Marked attachment %s from doc %s with attachment docID %s", compactionLoggingID, logger.UD(attachmentName), logger.UD(docID), logger.UD(attachmentDocID))
			markedAttachmentCount.Add(1)
		}
		return true
	}

	clientOptions := base.DCPClientOptions{
		OneShot:        true,
		FailOnRollback: true,
	}

	//	logger.InfofCtx(db.Ctx, logger.KeyAll, "[%s] Starting DCP feed for mark phase of attachment compaction", compactionLoggingID)
	logger.For(logger.UnknownKey).Info().Msgf("[%s] Starting DCP feed for mark phase of attachment compaction", compactionLoggingID)
	dcpFeedKey := compactionID + "_mark"
	dcpClient, err := base.NewDCPClient(dcpFeedKey, callback, clientOptions, db.Bucket, db.Options.GroupID)
	if err != nil {
		return 0, nil, err
	}

	doneChan, err := dcpClient.Start()
	if err != nil {
		_ = dcpClient.Close()
		return 0, nil, err
	}

	select {
	case <-doneChan:
		//		logger.InfofCtx(db.Ctx, logger.KeyAll, "[%s] Mark phase of attachment compaction completed. Marked %d attachments", compactionLoggingID, markedAttachmentCount.Value())
		logger.For(logger.UnknownKey).Info().Msgf("[%s] Mark phase of attachment compaction completed. Marked %d attachments", compactionLoggingID, markedAttachmentCount.Value())
		err = dcpClient.Close()
		if markProcessFailureErr != nil {
			return markedAttachmentCount.Value(), nil, markProcessFailureErr
		}
	case <-terminator.Done():
		err = dcpClient.Close()
		if markProcessFailureErr != nil {
			return markedAttachmentCount.Value(), nil, markProcessFailureErr
		}
		if err != nil {
			return markedAttachmentCount.Value(), base.GetVBUUIDs(dcpClient.GetMetadata()), err
		}

		err = <-doneChan
		if err != nil {
			return markedAttachmentCount.Value(), base.GetVBUUIDs(dcpClient.GetMetadata()), err
		}

		//		logger.InfofCtx(db.Ctx, logger.KeyAll, "[%s] Mark phase of attachment compaction was terminated. Marked %d attachments", compactionLoggingID, markedAttachmentCount.Value())
		logger.For(logger.UnknownKey).Info().Msgf("[%s] Mark phase of attachment compaction was terminated. Marked %d attachments", compactionLoggingID, markedAttachmentCount.Value())
	}

	return markedAttachmentCount.Value(), base.GetVBUUIDs(dcpClient.GetMetadata()), err
}

// AttachmentsMetaMap struct is a very minimal struct to unmarshal into when getting attachments from bodies
type AttachmentsMetaMap struct {
	Attachments map[string]AttachmentsMeta `json:"_attachments"`
}

// AttachmentCompactionData struct to unmarshal a document sync data into in order to process attachments during mark
// phase. Contains only what is necessary
type AttachmentCompactionData struct {
	Attachments map[string]AttachmentsMeta `json:"attachments"`
	Flags       uint8                      `json:"flags"`
	History     struct {
		BodyMap    map[string]string `json:"bodymap"`
		BodyKeyMap map[string]string `json:"BodyKeyMap"`
	} `json:"history"`
}

// getAttachmentSyncData takes the data type and data from the DCP feed and will return a AttachmentCompactionData
// struct containing data needed to process attachments on a document.
func getAttachmentSyncData(dataType uint8, data []byte) (*AttachmentCompactionData, error) {
	var attachmentData *AttachmentCompactionData
	var documentBody []byte

	if dataType&base.MemcachedDataTypeXattr != 0 {
		body, xattr, _, err := parseXattrStreamData(base.SyncXattrName, "", data)
		if err != nil {
			if errors.Is(err, base.ErrXattrNotFound) {
				return nil, nil
			}
			return nil, err
		}

		err = json.Unmarshal(xattr, &attachmentData)
		if err != nil {
			return nil, err
		}
		documentBody = body

	} else {
		type AttachmentDataSync struct {
			AttachmentData AttachmentCompactionData `json:"_sync"`
		}
		var attachmentDataSync AttachmentDataSync
		err := json.Unmarshal(data, &attachmentDataSync)
		if err != nil {
			return nil, err
		}

		documentBody = data
		attachmentData = &attachmentDataSync.AttachmentData
	}

	// If we've not yet found any attachments have a last effort attempt to grab it from the body for pre-2.5 documents
	if len(attachmentData.Attachments) == 0 {
		attachmentMetaMap, err := checkForInlineAttachments(documentBody)
		if err != nil {
			return nil, err
		}
		if attachmentMetaMap != nil {
			attachmentData.Attachments = attachmentMetaMap.Attachments
		}
	}

	return attachmentData, nil
}

// checkForInlineAttachments will scan a body for "_attachments" for pre-2.5 attachments and will return any attachments
// found
func checkForInlineAttachments(body []byte) (*AttachmentsMetaMap, error) {
	if bytes.Contains(body, []byte(BodyAttachments)) {
		var attachmentBody AttachmentsMetaMap
		err := json.Unmarshal(body, &attachmentBody)
		if err != nil {
			return nil, err
		}
		return &attachmentBody, nil
	}

	return nil, nil
}

// handleAttachments will iterate over the provided attachments and add any attachment doc IDs to the provided map
// Doesn't require an error return as if we fail at any point in here the attachment is either not a v1 attachment, or
// is unreadable which is likely unrecoverable.
func handleAttachments(attachmentKeyMap map[string]string, docKey string, attachmentsMap map[string]AttachmentsMeta) {
	for attName, attachmentMeta := range attachmentsMap {
		attMetaMap := attachmentMeta

		attVer, ok := GetAttachmentVersion(attMetaMap)
		if !ok {
			continue
		}

		if attVer != AttVersion1 {
			continue
		}

		digest, ok := attMetaMap["digest"]
		if !ok {
			continue
		}

		attKey := MakeAttachmentKey(AttVersion1, docKey, digest.(string))
		attachmentKeyMap[attName] = attKey
	}
}

func attachmentCompactSweepPhase(db *Database, compactionID string, vbUUIDs []uint64, dryRun bool, terminator *base.SafeTerminator, purgedAttachmentCount *base.AtomicInt) (int64, error) {
	//	logger.InfofCtx(db.Ctx, logger.KeyAll, "Starting second phase of attachment compaction (sweep phase) with compactionID: %q", compactionID)
	logger.For(logger.UnknownKey).Info().Msgf("Starting second phase of attachment compaction (sweep phase) with compactionID: %q", compactionID)
	compactionLoggingID := "Compaction Sweep: " + compactionID

	// Iterate over v1 attachments and if not marked with supplied compactionID we can purge the attachments.
	// In the event of an error we can return but continue - Worst case is an attachment which should be deleted won't
	// be deleted.
	callback := func(event sgbucket.FeedEvent) bool {
		docID := string(event.Key)

		// We only want to look over v1 attachment docs, skip otherwise
		if !strings.HasPrefix(docID, base.AttPrefix) {
			return true
		}

		// If the data contains an xattr then the attachment likely has a compaction ID, need to check this value
		if event.DataType&base.MemcachedDataTypeXattr != 0 {
			_, xattr, _, err := parseXattrStreamData(base.AttachmentCompactionXattrName, "", event.Value)
			if err != nil && !errors.Is(err, base.ErrXattrNotFound) {
				logger.For(logger.UnknownKey).Warn().Err(err).Msgf("[%s] Unexpected error occurred attempting to parse attachment xattr: %v", compactionLoggingID, err)
				return true
			}

			// If the document did indeed have an xattr then check the compactID. If it is the same as the current
			// running compaction ID we don't want to purge this doc and can continue to the next doc.
			if xattr != nil {
				var syncData map[string]interface{}
				err = json.Unmarshal(xattr, &syncData)
				if err != nil {
					logger.For(logger.UnknownKey).Warn().Err(err).Msgf("[%s] Failed to unmarshal xattr data: %v", compactionLoggingID, err)
					return true
				}

				compactIDSync, compactIDSyncPresent := syncData[CompactionIDKey]
				if _, compactionIDPresent := compactIDSync.(map[string]interface{})[compactionID]; compactIDSyncPresent && compactionIDPresent {
					return true
				}
			}
		}

		// If we've reached this point the current v1 attachment being processed either:
		// - Has no compactionID set in its xattr
		// - Has a compactionID set in its xattr but it is from a previous run and therefore is not equal to the passed
		// in compactionID
		// Therefore, we want to purge the doc (unless running as dryRun mode)
		if !dryRun {
			_, err := db.Bucket.Remove(docID, event.Cas)
			if err != nil {
				logger.For(logger.UnknownKey).Warn().Err(err).Msgf("[%s] Unable to purge attachment %s: %v", compactionLoggingID, logger.UD(docID), err)
				return true
			}
			//			logger.DebugfCtx(db.Ctx, logger.KeyAll, "[%s] Purged attachment %s", compactionLoggingID, logger.UD(docID))
			logger.For(logger.UnknownKey).Debug().Msgf("[%s] Purged attachment %s", compactionLoggingID, logger.UD(docID))
			db.DbStats.Database().NumAttachmentsCompacted.Add(1)
		} else {
			//			logger.DebugfCtx(db.Ctx, logger.KeyAll, "[%s] Would have purged attachment %s (not purged, running with dry run)", compactionLoggingID, logger.UD(docID))
			logger.For(logger.UnknownKey).Debug().Msgf("[%s] Would have purged attachment %s (not purged, running with dry run)", compactionLoggingID, logger.UD(docID))
		}

		purgedAttachmentCount.Add(1)
		return true
	}

	clientOptions := base.DCPClientOptions{
		OneShot:         true,
		FailOnRollback:  true,
		InitialMetadata: base.BuildDCPMetadataSliceFromVBUUIDs(vbUUIDs),
	}

	//	logger.InfofCtx(db.Ctx, logger.KeyAll, "[%s] Starting DCP feed for sweep phase of attachment compaction", compactionLoggingID)
	logger.For(logger.UnknownKey).Info().Msgf("[%s] Starting DCP feed for sweep phase of attachment compaction", compactionLoggingID)
	dcpFeedKey := compactionID + "_sweep"
	dcpClient, err := base.NewDCPClient(dcpFeedKey, callback, clientOptions, db.Bucket, db.Options.GroupID)
	if err != nil {
		return 0, err
	}

	doneChan, err := dcpClient.Start()
	if err != nil {
		_ = dcpClient.Close()
		return 0, err
	}

	select {
	case <-doneChan:
		//		logger.InfofCtx(db.Ctx, logger.KeyAll, "[%s] Sweep phase of attachment compaction completed. Deleted %d attachments", compactionLoggingID, purgedAttachmentCount.Value())
		logger.For(logger.UnknownKey).Info().Msgf("[%s] Sweep phase of attachment compaction completed. Deleted %d attachments", compactionLoggingID, purgedAttachmentCount.Value())
		err = dcpClient.Close()
	case <-terminator.Done():
		err = dcpClient.Close()
		if err != nil {
			return purgedAttachmentCount.Value(), err
		}

		err = <-doneChan
		if err != nil {
			return purgedAttachmentCount.Value(), err
		}

		//		logger.InfofCtx(db.Ctx, logger.KeyAll, "[%s] Sweep phase of attachment compaction was terminated. Deleted %d attachments", compactionLoggingID, purgedAttachmentCount.Value())
		logger.For(logger.UnknownKey).Info().Msgf("[%s] Sweep phase of attachment compaction was terminated. Deleted %d attachments", compactionLoggingID, purgedAttachmentCount.Value())
	}

	return purgedAttachmentCount.Value(), err
}

func attachmentCompactCleanupPhase(db *Database, compactionID string, vbUUIDs []uint64, terminator *base.SafeTerminator) error {
	//	logger.InfofCtx(db.Ctx, logger.KeyAll, "Starting third phase of attachment compaction (cleanup phase) with compactionID: %q", compactionID)
	logger.For(logger.UnknownKey).Info().Msgf("Starting third phase of attachment compaction (cleanup phase) with compactionID: %q", compactionID)
	compactionLoggingID := "Compaction Cleanup: " + compactionID

	callback := func(event sgbucket.FeedEvent) bool {

		docID := string(event.Key)

		if !strings.HasPrefix(docID, base.AttPrefix) {
			return true
		}

		if event.DataType&base.MemcachedDataTypeXattr == 0 {
			return true
		}

		_, xattr, _, err := parseXattrStreamData(base.AttachmentCompactionXattrName, "", event.Value)
		if err != nil && !errors.Is(err, base.ErrXattrNotFound) {
			logger.For(logger.UnknownKey).Warn().Err(err).Msgf("[%s] Unexpected error occurred attempting to parse attachment xattr: %v", compactionLoggingID, err)
			return true
		}

		if xattr != nil {
			// TODO: Struct map
			var attachmentCompactionMetadata map[string]map[string]interface{}
			err = json.Unmarshal(xattr, &attachmentCompactionMetadata)
			if err != nil {
				logger.For(logger.UnknownKey).Warn().Err(err).Msgf("[%s] Failed to unmarshal attachment compaction xattr: %v", compactionLoggingID, err)
				return true
			}

			// Get compactID map containing all compactIDs on the document, if one is not present for some reason we can
			// skip this
			compactIDSyncMap, compactIDSyncPresent := attachmentCompactionMetadata[CompactionIDKey]
			if !compactIDSyncPresent {
				return true
			}

			// Build up a set of compactionIDs that we can remove from the xattr. We always add the current
			// compaction ID as we're now done with it. Also check if any other compaction IDs are present. If any are
			// older than 30 days we can remove them.
			toDeleteCompactIDPaths := []string{getCompactionIDSubDocPath(compactionID)}
			for compactID, compactIDTimestampI := range compactIDSyncMap {
				if compactID == compactionID {
					continue
				}

				compactIDTimestampFloat, ok := compactIDTimestampI.(float64)
				if !ok {
					continue
				}

				compactIDTimestamp := time.Unix(int64(compactIDTimestampFloat), 0)
				diff := time.Now().UTC().Sub(compactIDTimestamp.UTC())
				if diff > time.Hour*24*30 {
					toDeleteCompactIDPaths = append(toDeleteCompactIDPaths, getCompactionIDSubDocPath(compactID))
				}
			}

			// If all the current compact IDs are to be deleted we can remove the entire attachment compaction xattr.
			// Note that if this operation fails with a cas mismatch we will fall through to the following per ID
			// delete. This can occur if another compact process ends up mutating / deleting the xattr.
			if len(compactIDSyncMap) == len(toDeleteCompactIDPaths) {
				err = db.Bucket.RemoveXattr(docID, base.AttachmentCompactionXattrName, event.Cas)
				if err == nil {
					return true
				}
				if err != nil && !base.IsCasMismatch(err) {
					logger.For(logger.UnknownKey).Warn().Err(err).Msgf("[%s] Failed to remove compaction ID xattr for doc %s: %v", compactionLoggingID, logger.UD(docID), err)
					return true
				}

			}

			// If we only want to remove select compact IDs delete each one through a subdoc operation
			err = db.Bucket.DeleteXattrs(docID, toDeleteCompactIDPaths...)
			if err != nil && !errors.Is(err, base.ErrXattrNotFound) {
				logger.For(logger.UnknownKey).Warn().Err(err).Msgf("[%s] Failed to delete compaction IDs %s for doc %s: %v", compactionLoggingID, strings.Join(toDeleteCompactIDPaths, ","), logger.UD(docID), err)
				return true
			}
		}

		return true
	}

	clientOptions := base.DCPClientOptions{
		OneShot:         true,
		FailOnRollback:  true,
		InitialMetadata: base.BuildDCPMetadataSliceFromVBUUIDs(vbUUIDs),
	}

	//	logger.InfofCtx(db.Ctx, logger.KeyAll, "[%s] Starting DCP feed for cleanup phase of attachment compaction", compactionLoggingID)
	logger.For(logger.UnknownKey).Info().Msgf("[%s] Starting DCP feed for cleanup phase of attachment compaction", compactionLoggingID)
	dcpFeedKey := compactionID + "_cleanup"
	dcpClient, err := base.NewDCPClient(dcpFeedKey, callback, clientOptions, db.Bucket, db.Options.GroupID)
	if err != nil {
		return err
	}

	doneChan, err := dcpClient.Start()
	if err != nil {
		_ = dcpClient.Close()
		return err
	}

	select {
	case <-doneChan:
		//		logger.InfofCtx(db.Ctx, logger.KeyAll, "[%s] Cleanup phase of attachment compaction completed", compactionLoggingID)
		logger.For(logger.UnknownKey).Info().Msgf("[%s] Cleanup phase of attachment compaction completed", compactionLoggingID)
		err = dcpClient.Close()
	case <-terminator.Done():
		err = dcpClient.Close()
		if err != nil {
			return err
		}

		err = <-doneChan
		if err != nil {
			return err
		}

		//		logger.InfofCtx(db.Ctx, logger.KeyAll, "[%s] Cleanup phase of attachment compaction was terminated", compactionLoggingID)
		logger.For(logger.UnknownKey).Info().Msgf("[%s] Cleanup phase of attachment compaction was terminated", compactionLoggingID)
	}

	return err
}

// getCompactionIDSubDocPath is just a tiny helper func that just concatenates the subdoc path we're using to store
// compactionIDs
func getCompactionIDSubDocPath(compactionID string) string {
	return base.AttachmentCompactionXattrName + "." + CompactionIDKey + "." + compactionID
}
