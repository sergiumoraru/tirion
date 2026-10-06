import { ServiceBus } from './queue-client'
import * as config from '../core/config.json'

const serviceBus = new ServiceBus('account', 'key', 'name')
serviceBus.load(config.APP_PUBLISH_DOCUMENT_QUEUE)

function submitForm(url, payload) {
  const form = document.createElement('form')
  form.method = 'POST'
  form.action = url
  form.target = '_blank'
  form.submit()
}

export async function httpTrigger(context, req) {
  await serviceBus.scheduleMessages([{ body: { id: '1' } }], new Date(), context)
}

export function pdfExport(productName, filename) {
  const apimUrl = 'https://apim.example.com'
  const exportPdfUrl = `${apimUrl}/reports/${productName}/export-audit/${filename}`
  submitForm(exportPdfUrl, { fileType: 'pdf' })
}
