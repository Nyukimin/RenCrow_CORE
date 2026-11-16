package main

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	gmailapp "github.com/Nyukimin/RenCrow_CORE/internal/application/gmailintake"
)

type gmailBoardDiaryWriter struct{ client *boardcommunication.Client }

func (w *gmailBoardDiaryWriter) Close() error {
	if w == nil || w.client == nil {
		return nil
	}
	w.client.Close()
	w.client = nil
	return nil
}

func newGmailBoardDiaryWriter(board config.BoardCommunicationConfig) (*gmailBoardDiaryWriter, error) {
	credentials, err := config.ReadBoardDiaryImportCredentials(board.DiaryImportTokenFile)
	if err != nil {
		return nil, errors.New("diary import credentials are unavailable")
	}
	options := boardcommunication.Options{
		URL: strings.TrimSpace(board.URL), UnixSocket: strings.TrimSpace(board.UnixSocket),
		ExpectedConnection: boardcommunication.ConnectionIdentity{PrincipalID: credentials.ExpectedConnection.PrincipalID, JournalInstanceID: credentials.ExpectedConnection.JournalInstanceID},
		TokenSource:        func(context.Context) ([]byte, error) { return []byte(credentials.Token), nil },
	}
	client, err := boardcommunication.NewClient(options)
	if err != nil {
		return nil, errors.New("diary import BOARD client is invalid")
	}
	return &gmailBoardDiaryWriter{client: client}, nil
}

func (w *gmailBoardDiaryWriter) GetDiary(ctx context.Context, date string) (gmailapp.GmailDiary, bool, error) {
	if w == nil || w.client == nil {
		return gmailapp.GmailDiary{}, false, errors.New("BOARD diary writer unavailable")
	}
	entry, found, err := w.client.GetLuminaImportDiary(ctx, date)
	if err != nil {
		return gmailapp.GmailDiary{}, false, err
	}
	if !found {
		return gmailapp.GmailDiary{}, false, nil
	}
	if entry.Revision > math.MaxInt {
		return gmailapp.GmailDiary{}, false, errors.New("BOARD diary revision exceeds CORE bound")
	}
	return gmailapp.GmailDiary{ActorID: entry.ActorID, Date: entry.Date, Revision: int(entry.Revision), Body: entry.Body, Reflection: entry.Reflection}, true, nil
}

func (w *gmailBoardDiaryWriter) PutDiary(ctx context.Context, diary gmailapp.GmailDiary) (gmailapp.GmailDiary, error) {
	if w == nil || w.client == nil || diary.Revision < 1 {
		return gmailapp.GmailDiary{}, errors.New("BOARD diary writer unavailable")
	}
	_, err := w.client.PutLuminaImportDiary(ctx, boardcommunication.DiaryWrite{ActorID: diary.ActorID, Date: diary.Date, Revision: int64(diary.Revision), Body: diary.Body, Reflection: diary.Reflection})
	if err != nil {
		return gmailapp.GmailDiary{}, err
	}
	return diary, nil
}
