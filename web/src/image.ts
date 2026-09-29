export const MAX_IMAGE_BYTES = 500 * 1024

export interface EncodedImage { mime: string; base64: string }

const MAX_SIDE = 1920
const MIME = 'image/jpeg'

// Decodes an image file, downscales it to a sane size and re-encodes as JPEG,
// tightening quality and then dimensions until it fits MAX_IMAGE_BYTES.
export async function encodeImageFile(file: File): Promise<EncodedImage> {
  if (!file.type.startsWith('image/')) throw new Error('NOT_IMAGE')
  const bitmap = await createImageBitmap(file)
  try {
    const scale = Math.min(1, MAX_SIDE / Math.max(bitmap.width, bitmap.height))
    let width = Math.max(1, Math.round(bitmap.width * scale))
    let height = Math.max(1, Math.round(bitmap.height * scale))
    const canvas = document.createElement('canvas')
    const context = canvas.getContext('2d')
    if (!context) throw new Error('CANVAS_UNAVAILABLE')

    let quality = 0.85
    let bytes = await render(canvas, context, bitmap, width, height, quality)
    while (bytes.length > MAX_IMAGE_BYTES && quality > 0.4) {
      quality -= 0.1
      bytes = await render(canvas, context, bitmap, width, height, quality)
    }
    while (bytes.length > MAX_IMAGE_BYTES && Math.max(width, height) > 480) {
      width = Math.max(1, Math.round(width * 0.8))
      height = Math.max(1, Math.round(height * 0.8))
      bytes = await render(canvas, context, bitmap, width, height, quality)
    }
    if (bytes.length > MAX_IMAGE_BYTES) throw new Error('IMAGE_TOO_LARGE')
    return { mime: MIME, base64: toBase64(bytes) }
  } finally {
    bitmap.close()
  }
}

async function render(canvas: HTMLCanvasElement, context: CanvasRenderingContext2D, source: ImageBitmap, width: number, height: number, quality: number): Promise<Uint8Array> {
  canvas.width = width
  canvas.height = height
  context.fillStyle = '#ffffff'
  context.fillRect(0, 0, width, height)
  context.drawImage(source, 0, 0, width, height)
  const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, MIME, quality))
  if (!blob) throw new Error('ENCODE_FAILED')
  return new Uint8Array(await blob.arrayBuffer())
}

function toBase64(bytes: Uint8Array): string {
  let binary = ''
  const chunk = 0x2000
  for (let index = 0; index < bytes.length; index += chunk) {
    binary += String.fromCharCode(...bytes.subarray(index, index + chunk))
  }
  return btoa(binary)
}
