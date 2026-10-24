const newPath = '/admin/inboxes/new'

const openNewForm = () => {
  cy.visit(newPath)
  cy.contains('button', 'WhatsApp').click()
}

describe('WhatsApp inbox form', () => {
  beforeEach(() => {
    cy.viewport(1280, 800)
    cy.login()
  })

  it('shows the WAGO inbox fields and device panel', () => {
    openNewForm()

    cy.get('input[name="name"]').should('exist')
    cy.get('input[name="reopen_window_hours"]').should('exist')
    cy.contains('WAGO device').should('be.visible')
    cy.contains('Connect with QR').should('exist')
  })

  it('keeps the CSAT message hidden until CSAT surveys are on', () => {
    openNewForm()

    cy.get('textarea[name="config.csat_message"]').should('not.be.visible')
    cy.contains('CSAT Surveys').parent().find('button[role="switch"]').click()
    cy.get('textarea[name="config.csat_message"]').should('be.visible')
  })

  it('rejects a submit with no name', () => {
    cy.intercept('POST', '**/api/v1/inboxes').as('createInbox')

    openNewForm()
    cy.get('button[type="submit"]').click()

    cy.get('input[name="name"]').scrollIntoView()
    cy.contains(/required/i).should('be.visible')
    cy.get('@createInbox.all').should('have.length', 0)
    cy.location('pathname').should('eq', newPath)
  })
})
