# Sending email

Add email under **Settings > Connections**, then press **Test**. A rule only sends to a
connection you tick in that rule.

## Gmail or Google Workspace

Google refuses your normal password here, so you need an app password. These steps also
appear in the connection form.

1. In your browser, sign in to the Google account that will send the alerts.
2. Open [2-Step Verification](https://myaccount.google.com/signinoptions/two-step-verification).
   If it's off, turn it on and follow the steps on screen. You need this before Google offers
   app passwords.
3. Open [App passwords](https://myaccount.google.com/apppasswords). Google may ask you to sign
   in again.
4. Type a name you'll recognize, such as Seelynx, and choose **Create**.
5. Copy the 16-character password Google shows. You can't view it again later, but you can
   always create a new one.
6. In Seelynx, open **Settings > Connections > Add connection** and choose **Gmail or Google
   Workspace**.
7. Enter the Gmail address and paste the app password, with or without the spaces.
8. Enter the addresses that should receive alerts, separated by commas.
9. Choose **Save connection**, then press **Test** and check that the message arrives.
10. Tick the connection in each rule that should send email.

**If there's no App passwords page,** Google doesn't offer it when:

- 2-Step Verification uses only security keys. Add another method, such as an authenticator
  app.
- The account has Advanced Protection. Use a different account for alerts.
- It's a work, school or other organization account. Ask your admin.

**If Test says the username and password weren't accepted,** check that you pasted the app
password and not your normal password, and that the address belongs to the account that
created it.

**If email stops later,** check whether the account's Google password changed, because that
cancels its app passwords. Create a new one, edit the connection and paste it in.

**To stop Seelynx sending,** remove its entry from the
[App passwords](https://myaccount.google.com/apppasswords) page. It can't use that password
again.

Use an account made for alerts, not a personal one. Google limits how much one account can
send each day, and a busy motion rule could reach that limit, so set a sensible wait between
alerts.

## Microsoft 365

Seelynx sends through Microsoft Graph. Microsoft is retiring password sign-in for mail
programs, so this connection doesn't use a mailbox password. An admin sets it up once, and
these steps also appear in the connection form.

Create a mailbox for alerts first. A shared mailbox is a good choice.

### Register the app

1. Sign in to the [Microsoft Entra admin center](https://entra.microsoft.com) as an admin of
   your organization.
2. Go to **Entra ID > App registrations** and choose **New registration**.
3. Name it Seelynx, choose **Single tenant only** under **Supported account types**, leave the
   redirect address empty, and choose **Register**.
4. On the **Overview** page, copy the **Application (client) ID** and the **Directory (tenant)
   ID**.
5. Choose **Certificates & secrets > Client secrets > New client secret**.
6. Add a description, pick an expiry of up to 24 months, and choose **Add**.
7. Copy the secret's **Value** straight away, because it's never shown again. The Secret ID is
   not what you need.

### Add the connection

1. In Seelynx, open **Settings > Connections > Add connection** and choose **Microsoft 365
   email**.
2. Paste the three values, then enter the mailbox to send from and the addresses to send to.
3. Choose **Save connection**. Don't press **Test** yet, because the app has no permission.

### Give the app permission

Use **one** of these options, not both. Microsoft adds the two kinds of permission together,
so if you use both, the app can send as anyone and the limit in Option A has no effect.

**Option A: one mailbox only (recommended)**

You need to be a member of the Exchange **Organization Management** role group.

1. In the Entra admin center, go to **Entra ID > Enterprise applications** and open Seelynx.
2. Copy its **Application ID** and **Object ID**. Use these, not the IDs on the App
   registrations page, which are different.
3. In PowerShell, run these commands with your IDs and mailbox:

   ```powershell
   Install-Module ExchangeOnlineManagement
   Connect-ExchangeOnline

   New-ServicePrincipal -AppId <Application ID> -ObjectId <Object ID> -DisplayName "Seelynx"
   New-ManagementScope -Name "Seelynx mailbox" -RecipientRestrictionFilter "PrimarySmtpAddress -eq 'alerts@example.com'"
   New-ManagementRoleAssignment -Name "Seelynx mail" -Role "Application Mail.Send" -App <Object ID> -CustomResourceScope "Seelynx mailbox"

   Test-ServicePrincipalAuthorization -Identity <Object ID> -Resource alerts@example.com
   ```

4. Check that the last command shows **InScope** as True.
5. Wait before pressing **Test**. Microsoft says a change can take from 30 minutes to 2 hours
   to apply.

**Option B: whole organization (quicker, less safe)**

Anyone who gets this client secret could send email as any person in your organization. You
need an admin who can grant consent, such as a Global Administrator.

1. In **App registrations**, open Seelynx and go to **API permissions**.
2. Choose **Add a permission > Microsoft Graph > Application permissions**.
3. Tick **Mail.Send** and choose **Add permissions**.
4. Choose **Grant admin consent** for your organization, then **Yes**.

### Finish

1. Press **Test** on the connection and check that the message arrives.
2. Tick the connection in each rule that should send email.
3. Put the secret's expiry date in your calendar.

### If something goes wrong

- **"Microsoft refused the sign-in":** check the tenant ID, the client ID and the secret. Make
  sure you copied the secret's Value, not its Secret ID, and that it hasn't expired.
- **"Microsoft refused the message (401 or 403)":** the app doesn't have permission for this
  mailbox yet. With Option A, wait up to 2 hours, then run the test command again.
- **"Microsoft refused the message (404)":** check the mailbox address.
- **Email stops later:** the secret has probably expired. Failed deliveries appear in the event
  log. Create a new secret, edit the connection and paste its Value.

Alerts are not kept in the mailbox's Sent Items.

Personal Outlook.com accounts can't use this connection, because they have no Entra directory
to register the app in.

## Other mail servers

Use **Email (other mail server)**. Port 587 upgrades to an encrypted connection when the
server offers it, and port 465 is encrypted from the start. A password is never sent over an
unencrypted connection, so a server without encryption only works without a login.
