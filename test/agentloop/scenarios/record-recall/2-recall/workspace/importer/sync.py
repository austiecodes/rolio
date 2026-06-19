import time


def sync_account(client, store, account):
    cursor = None
    while True:
        page = client.list_invoices(account=account, cursor=cursor)
        if page.status == 429:
            time.sleep(120)
            continue
        store.save(page.invoices)
        if not page.next_cursor:
            return
        cursor = page.next_cursor
