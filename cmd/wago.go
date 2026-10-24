package main

import (
	"context"
	"time"

	"github.com/abhinavxd/libredesk/internal/envelope"
	"github.com/abhinavxd/libredesk/internal/wago"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const wagoCallTimeout = 30 * time.Second

func wagoClientOrError(r *fastglue.Request, app *App) (*wago.Client, error) {
	if app.wagoClient == nil || !app.wagoClient.Configured() {
		return nil, envelope.NewError(envelope.InputError, app.i18n.T("admin.inbox.whatsapp.error.notConfigured"), nil)
	}
	return app.wagoClient, nil
}

func handleWagoDevice(r *fastglue.Request) error {
	app := r.Context.(*App)
	client, err := wagoClientOrError(r, app)
	if err != nil {
		return sendErrorEnvelope(r, err)
	}
	ctx, cancel := context.WithTimeout(r.RequestCtx, wagoCallTimeout)
	defer cancel()
	state, err := client.DeviceState(ctx)
	if err != nil {
		app.lo.Error("error fetching wago device state", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, err.Error(), nil, envelope.GeneralError)
	}
	return r.SendEnvelope(state)
}

func handleWagoLogin(r *fastglue.Request) error {
	app := r.Context.(*App)
	client, err := wagoClientOrError(r, app)
	if err != nil {
		return sendErrorEnvelope(r, err)
	}
	ctx, cancel := context.WithTimeout(r.RequestCtx, wagoCallTimeout)
	defer cancel()
	if st, err := client.Status(ctx); err == nil && st.IsLoggedIn {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, app.i18n.T("admin.inbox.whatsapp.device.alreadyLoggedIn"), nil, envelope.InputError)
	}
	qr, err := client.Login(ctx)
	if err != nil {
		app.lo.Error("error starting wago login", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, err.Error(), nil, envelope.GeneralError)
	}
	return r.SendEnvelope(qr)
}

func handleWagoReconnect(r *fastglue.Request) error {
	app := r.Context.(*App)
	client, err := wagoClientOrError(r, app)
	if err != nil {
		return sendErrorEnvelope(r, err)
	}
	ctx, cancel := context.WithTimeout(r.RequestCtx, wagoCallTimeout)
	defer cancel()
	if err := client.Reconnect(ctx); err != nil {
		app.lo.Error("error reconnecting wago device", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, err.Error(), nil, envelope.GeneralError)
	}
	return r.SendEnvelope(true)
}

func handleWagoLogout(r *fastglue.Request) error {
	app := r.Context.(*App)
	client, err := wagoClientOrError(r, app)
	if err != nil {
		return sendErrorEnvelope(r, err)
	}
	ctx, cancel := context.WithTimeout(r.RequestCtx, wagoCallTimeout)
	defer cancel()
	if err := client.Logout(ctx); err != nil {
		app.lo.Error("error logging out wago device", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, err.Error(), nil, envelope.GeneralError)
	}
	return r.SendEnvelope(true)
}
