describe('API: whatsapp', () => {
  before(() => cy.login())
  beforeEach(() => cy.login())

  describe('webhook endpoint', () => {
    it('rejects a delivery aimed at a non-numeric inbox id', () => {
      cy.request({
        method: 'POST',
        url: '/webhooks/whatsapp/not-an-id',
        body: { event: 'message', payload: {} },
        failOnStatusCode: false
      }).then(({ status, body }) => {
        expect(status).to.eq(400)
        expect(body.error_type).to.eq('InputException')
      })
    })

    it('does not ask for a session on the public webhook', () => {
      cy.request({
        method: 'POST',
        url: '/webhooks/whatsapp/1',
        body: { event: 'message', payload: {} },
        failOnStatusCode: false
      }).then(({ status }) => {
        expect(status).to.not.eq(401)
      })
    })
  })

  describe('WAGO device API', () => {
    it('requires authentication', () => {
      cy.request({
        method: 'GET',
        url: '/api/v1/wago/device',
        failOnStatusCode: false
      }).then(({ status }) => {
        expect(status).to.eq(401)
      })
    })
  })
})
