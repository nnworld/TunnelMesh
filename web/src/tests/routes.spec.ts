import { describe, expect, it } from 'vitest'
import router from '../router'

describe('admin routes', () => {
  it('defines login and managed resources', () => {
    const paths = router.getRoutes().map(route => route.path)
    expect(paths).toEqual(expect.arrayContaining(['/login', '/', '/agents', '/routes', '/tunnels', '/audit-logs']))
  })
})
