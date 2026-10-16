// The code the fixtures' errors come from: showPrice fails on a missing product.
export interface Product {
  name: string
  price: number
}

export function showPrice(product: Product | undefined) {
  const label = document.querySelector<HTMLElement>('#price')!
  label.textContent = product!.price.toFixed(2)
}

export function onCheckoutClick() {
  const cart: Product[] = []
  showPrice(cart[0])
}

export function onFreeClick() {
  showPrice({ name: 'Sticker', price: 0 })
}
