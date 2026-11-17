import type { ReactNode } from 'react'
import type { Permission } from './api'

// All in-app help lives here, so it can be kept up to date in one place.
// Each page shows its own topic, and the Help page lists every topic the user can use.

interface Topic {
  title: string
  perm?: Permission
  intro?: ReactNode
  steps?: ReactNode[]
  more?: ReactNode
  notes?: ReactNode[]
}

const Link = ({ to, children }: { to: string; children: ReactNode }) => <a href={to} target="_blank" rel="noreferrer">{children}</a>

function Option({ title, steps, note }: { title: string; steps: ReactNode[]; note?: ReactNode }) {
  return (
    <details>
      <summary>{title}</summary>
      <ol>{steps.map((s, i) => <li key={i}>{s}</li>)}</ol>
      {note && <p className="hint">{note}</p>}
    </details>
  )
}

const exchangeCommands = `Install-Module ExchangeOnlineManagement
Connect-ExchangeOnline

New-ServicePrincipal -AppId <Application ID> -ObjectId <Object ID> -DisplayName "Seelynx"
New-ManagementScope -Name "Seelynx mailbox" -RecipientRestrictionFilter "PrimarySmtpAddress -eq 'alerts@example.com'"
New-ManagementRoleAssignment -Name "Seelynx mail" -Role "Application Mail.Send" -App <Object ID> -CustomResourceScope "Seelynx mailbox"

Test-ServicePrincipalAuthorization -Identity <Object ID> -Resource alerts@example.com`

const hookExample = `curl -X POST -H "Content-Type: application/json" -d '{"message":"Door 3 forced"}' <the rule's web address>`

const webhookBody = `{
  "title": "Seelynx: Front door",
  "text": "Motion on Front door at 14:32:05",
  "priority": "medium",
  "event": "motion",
  "camera": "Front door",
  "camera_id": 7,
  "at": "2026-11-05T14:32:05-08:00",
  "snapshot": "/9j/4AAQSkZJRg..."
}`

const thenRule = 'Under Settings > Rules, tick this connection in each rule that should use it.'

export const topics: Record<string, Topic> = {
  start: {
    title: 'Getting started',
    intro: 'Seelynx records your cameras, shows them live, and tells you when something happens. What you can see here depends on your role.',
    steps: [
      'Add your cameras on the Cameras page. Find ONVIF cameras fills in the stream addresses for you.',
      'Check that each camera shows Online, then watch it on the Live page.',
      'Set how long to keep recordings for each camera, and add a recording schedule if you don\'t want to record all the time.',
      'Add a user for each person on the Users page, instead of sharing one account.',
      'To get alerts, add a connection under Settings > Connections, then add a rule under Settings > Rules.',
      'To use a camera\'s analytics by name, connect your AI service and build a driver under Settings > Drivers.',
      'Under Settings > Backup and restore, keep a copy of data\\secret.key somewhere safe.',
    ],
  },
  live: {
    title: 'Help: Live view',
    perm: 'live',
    steps: [
      'Choose how many tiles you want with the numbers above the grid.',
      'Click a camera in the list to put it in the next empty tile, or drag it onto a tile.',
      'Double-click a tile to fill the screen with that camera. Choose Back to grid to return.',
      'Use the × on a tile to take that camera out of the grid.',
      'Use the shape list on a tile to change how that camera\'s picture fits, and Playback to open that camera\'s recordings.',
      'Choose Save view to keep a layout, then reopen it from the view list. Run a tour to rotate through saved views.',
    ],
    notes: [
      'Dots in the camera list: green is online, grey is Standby, which means the camera connects when someone watches it, and red is offline.',
      'A Motion badge on a tile means the camera is reporting motion right now.',
      'Grids use the camera\'s sub stream when it has one, and a single tile uses the main stream for full quality.',
      'Picture shapes: Original shows the whole picture with bars if needed, Fill covers the tile and crops the edges, Stretch covers the tile and distorts the picture, and 16:9 and 4:3 force that shape. This only changes the display here, not the camera or its recordings, and each browser remembers its own choices.',
      'A fisheye camera has a view list on its tile instead: Panorama, Virtual camera or Circle. In Virtual camera, drag the picture to look around and scroll to zoom. A panorama of a ceiling camera turns when you drag it sideways. Each browser remembers each camera\'s view.',
    ],
  },
  playback: {
    title: 'Help: Playback',
    perm: 'playback',
    steps: [
      'Choose a camera and a date.',
      'Blue on the timeline shows recorded video, and amber along the top shows motion.',
      'Click the timeline to start playing from that time.',
      'Use Play, Slow motion and Pause in the middle. The buttons on either side go back or forward 10 seconds. Slow motion plays at a quarter of normal speed, and the speed buttons on the left play faster. At 4× and 8× you see about one picture for each second of video, which is how fast playback stays smooth.',
      'For a fisheye camera, choose Panorama, Virtual camera or Circle next to the date. In Virtual camera, drag the picture to look around, even while paused.',
    ],
    more: (
      <>
        <Option title="Zoom in on the timeline" steps={[
          'Scroll the mouse wheel over the timeline, or use the − and + buttons. You can zoom in to one minute.',
          'Drag the timeline to move along it.',
          'Choose Fit to see the whole day again.',
        ]} note="The pointer shows what will happen: a pointing hand jumps to that time, an open hand also lets you drag the timeline, a crosshair selects a section while Shift is held, and a two-way arrow moves a handle. With the timeline selected, the + and − keys zoom, the arrow keys move along it, and 0 shows the whole day." />
        <Option title="Export a video" steps={[
          'Play to a moment and choose Mark start. This marks 3 minutes from that moment, and zooms in if the section would be too small to adjust. You can also hold Shift and drag across the timeline.',
          'Drag the handles on either side of the section to change it, or type the From and To times.',
          'Choose Export video. The file downloads as a standard MP4 that plays almost anywhere.',
        ]} note="Exports can be up to 6 hours, and each one is noted under Recent events on the System page. If the range has gaps, the recorded parts are joined into one file, so the video jumps at each gap. Large exports can take a while to start." />
      </>
    ),
    notes: [
      'No blue on the timeline means nothing was recorded that day. Check that the camera has Record ticked, and look at its schedule and how long it keeps recordings.',
      'Times use this computer\'s clock and time zone.',
    ],
  },
  alarms: {
    title: 'Help: Alarms',
    perm: 'system',
    intro: 'An alarm appears when a rule with Raise an alarm on screen fires. The number in the menu shows how many are open.',
    steps: [
      'Read the alarm, then check the camera on the Live or Playback page.',
      'Add a note saying what you found. This is optional, but it helps the next person.',
      'Choose Acknowledge. The alarm moves to the Acknowledged list with your name and the note.',
    ],
    notes: [
      'High alarms have a red edge, medium amber and low blue.',
      'A tone plays when a new alarm arrives, but browsers block sound until you\'ve clicked somewhere on the page.',
      'If the rule includes a snapshot, the picture appears a few seconds after the alarm. Click it to see it full size. Pictures are kept for 30 days, and the oldest are removed sooner if they pass 2 GB in total.',
      'Seeing pictures needs the Live view or Playback permission as well.',
      'You only see alarms for cameras your role allows.',
    ],
  },
  cameras: {
    title: 'Help: Cameras',
    perm: 'cameras',
    intro: 'Each camera needs at least a main stream address. If the camera supports ONVIF, Seelynx can find that for you.',
    steps: [
      'Choose Find ONVIF cameras, then Scan local network. If nothing answers, type the device address yourself. Cameras you have already added are marked Added as, with their name.',
      'Pick the camera, enter its username and password, or use the default login, and choose Read streams.',
      'Check the form that opens, give the camera a name, and choose Add camera.',
      'To add a camera without ONVIF, choose Add camera and enter its RTSP address from the maker\'s manual.',
      'Wait for the status to show Online.',
    ],
    more: (
      <>
      <Option title="Set a default camera login" steps={[
        'Choose Default login, enter the username and password your cameras share, and choose Save default.',
        'When you add a camera, leave Use the default login ticked. Untick it to enter a different login, or none.',
        'To change the login of one camera later, choose Edit on that camera and enter the new username and password.',
      ]} note="Each camera gets its own copy of the default login when you add it. If you change the default later, cameras you've already added keep the login they have. The password is stored encrypted and is never shown again." />
      <Option title="Set up a fisheye camera" steps={[
        'Choose Edit on the camera, and wait for its picture.',
        'Tick This camera has a fisheye lens. Seelynx finds the circle in the picture and draws it in red. If the red line doesn\'t sit on the edge of the picture, adjust the circle\'s centre and radius.',
        'Choose how it\'s mounted: on a wall, on the ceiling, or on a desk or the floor.',
        'Check the preview. For a wall camera, set Tilt down until the horizon is level. If straight lines still bend, change the lens angle a few degrees at a time.',
        'Choose Save changes.',
      ]} note="Live and Playback then show the camera as a panorama. Each tile's view list also offers Virtual camera, which looks like a normal camera that you aim by dragging and zoom with the scroll wheel, and Circle, which shows the picture as the camera sends it. The recording always keeps the whole circle, so you can look anywhere in recorded video too. Saving the lens doesn't restart the camera." />
      <Option title="Turn a camera's picture" steps={[
        'Choose Edit on a camera that is mounted on its side or upside down.',
        'Under Turn the picture, choose a quarter turn either way, or Upside down. The picture above turns to match.',
        'Choose Save changes.',
      ]} note="Live, Playback and the Cameras list then show it turned, for everyone. The camera and its recording are left as they are, and the camera doesn't restart. A fisheye camera's panorama and virtual camera are turned too, so set the turn before adjusting the lens." />
      </>
    ),
    notes: [
      'Online means video is arriving. Standby means the camera isn\'t recording and connects when someone watches it. Offline means Seelynx can\'t reach it, so check its address, login and network.',
      'Each camera shows a picture of what it sees, to help you tell where it is. Pictures are updated every few minutes, and a camera that is offline shows its last one. Choose Edit, then Take a new picture, to see it now.',
      'Motion events need a camera with ONVIF and a login.',
      'On motion only keeps the video around motion and deletes the rest, which saves a lot of disk space. Seelynx records all the time and then removes what it doesn\'t need, which is how it can keep video from before the motion. Video recorded before you turned it on isn\'t touched.',
      'On motion only is careful with your video. Everything is kept until the camera has reported motion at least once, and again whenever motion events stop arriving. If that happens, a warning appears on the System page. Try it by walking past the camera, then check Playback.',
      'Removing a camera doesn\'t delete its recordings straight away, but you can no longer play them back. They stay on disk until they age out.',
    ],
  },
  users: {
    title: 'Help: Users',
    perm: 'users',
    steps: [
      'Fill in a username and a password of at least 10 characters.',
      'Choose a role, which sets what this person can do and which cameras they can see.',
      'Leave Must change password at next sign-in ticked, so only they know the password.',
      'Choose Add user, then give them the username and password.',
      'To reset a forgotten password, choose Edit, enter a new password and save.',
    ],
    notes: [
      'Duplicate starts a new user with the same role and options.',
      'Use Account expires for temporary staff and contractors.',
      'You can\'t remove your own account.',
    ],
  },
  roles: {
    title: 'Help: Roles',
    perm: 'users',
    intro: 'A role sets what people can do. The built-in roles are Administrator (everything), Operator and Viewer, and they can\'t be changed.',
    steps: [
      'Name the role after the job, such as Reception or Night guard.',
      'Tick what this role can do. Leave out anything it doesn\'t need.',
      'To limit which cameras it can see, untick All cameras and tick the ones it needs.',
      'Choose Add role, then assign it to people on the Users tab.',
    ],
    notes: [
      'Manage users can grant any access, so only give it to people you trust as administrators.',
      'Changes apply straight away, including for people already signed in.',
    ],
  },
  system: {
    title: 'Help: System',
    perm: 'system',
    intro: 'This page shows whether the system is healthy. It updates by itself every few seconds.',
    notes: [
      'Cameras online counts cameras that are sending video. Cameras on Standby aren\'t counted until someone watches them.',
      'Incoming video is the total arriving from all cameras.',
      'Disk free adds up every storage location that can be read now. When a location runs low, Seelynx deletes its oldest recordings first, even if they\'re newer than the number of days you set. See each location on the Storage page.',
      'Recent events shows cameras going offline, failed alert deliveries and other warnings. Check here first when something stops working.',
    ],
  },
  storage: {
    title: 'Help: Storage',
    perm: 'settings',
    intro: 'Storage locations are the folders that hold video. With more than one, Seelynx decides where each camera records, and Playback shows video from all of them.',
    steps: [
      'Choose Add location, and give it a name.',
      'Choose Browse… to pick a folder on the Seelynx server, or type its full path, such as D:\\Video or /mnt/nas/video. The dialog can also make a new folder.',
      'Set Keep free: when the disk gets this full, the oldest video in this location is deleted.',
      'Choose Add location. Seelynx checks it can write there before using it.',
    ],
    more: (
      <>
        <Option title="How Seelynx shares cameras between locations" steps={[
          'Each camera records to one location at a time, so its video is never split mid-recording.',
          'Cameras are spread out by size: a location with twice the room gets about twice the cameras. Cameras you\'ve already added stay where they are when you add a location, and new cameras go where there\'s most room.',
          'If a location stops working, such as a network share going offline, its cameras record to another location within about 30 seconds. They go back 2 minutes after it\'s working again.',
          'Each location deletes its own oldest video when its disk reaches Keep free, so a full location doesn\'t delete video from another one.',
        ]} note="Moves and outages are listed under Recent events on the System page, and the Storage problem trigger in Rules can alert you." />
        <Option title="Use a network share (NAS)" steps={[
          'On Windows, a mapped drive letter only exists for the person who mapped it. Use the share\'s full path, such as \\\\nas\\video.',
          'Enter the Share username and Share password of a NAS account that can change the folder. Seelynx signs in by itself, and again after a restart or when the NAS comes back. The password is stored encrypted.',
          'All locations on the same NAS have to use the same username, because Windows allows only one login per NAS for each account.',
          'On Linux or Docker, mount the share first, so it\'s connected after a restart, and enter the mounted folder. With Docker, add the folder to the container in docker-compose.yml and enter the path inside the container.',
          'Seelynx puts a small file called .seelynx-storage in each location. If a share isn\'t connected and only an empty folder is left behind, the file is missing, so Seelynx records somewhere else instead of filling the wrong disk.',
        ]} />
        <Option title="Keep recording while the network storage is down" steps={[
          'Add a location on a local disk, such as C:\\SeelynxStandby, and tick Standby.',
          'Cameras never record to a standby location normally. If their own location fails, they go to another working location first, and to standby only when none is left.',
          'When a network location is back, cameras leave standby. Once nothing is recording to standby, its video is moved to each camera\'s own location, a file at a time, and can be played back throughout.',
          'The standby location stays, empty and ready for the next outage. Set its Keep free so an outage can\'t fill the local disk.',
        ]} />
        <Option title="Retire or remove a location" steps={[
          'To stop recording to a disk but keep its video, choose Edit and untick Record new video here. Its cameras move to other locations, and its video can still be played back until it\'s deleted by age.',
          'To empty a location before removing it, choose Remove, then Move its video to, and pick where it goes. Seelynx checks there\'s room, stops recording there, and moves the video in the background with its progress on this page. Video can be played back throughout, and the location is removed once it\'s empty. Stop moving keeps it, holding whatever wasn\'t moved yet.',
          'Moving pauses while either location is unavailable, and carries on after a restart. Moving to another disk or a network share copies each file, so a lot of video can take hours.',
          'Or choose Remove it now to stop using a location straight away. The files stay on the disk, but they aren\'t in Playback unless you add the same folder again.',
          'At least one location that isn\'t on standby has to record new video.',
        ]} />
      </>
    ),
    notes: [
      'Two locations on the same disk share its free space, so there\'s no benefit in having more than one per disk.',
      'Changing a location\'s folder doesn\'t move the video. Move the files yourself first.',
    ],
  },
  account: {
    title: 'Help: Your password',
    intro: 'Click your username at the top of the page to change your password. If you\'ve forgotten it, ask an administrator to set a new one.',
  },
  rules: {
    title: 'Help: Rules',
    perm: 'settings',
    intro: 'A rule says what to do when something happens. It can raise an alarm on screen, send a message through your connections, or both.',
    steps: [
      'Add a connection first if you want messages sent outside Seelynx.',
      'Choose Add rule, give it a name, and choose what it reacts to under When.',
      'Choose cameras if you want to limit it. Ticking none means all cameras.',
      'Set Only during if it should only work at certain times, such as outside business hours.',
      'Set the wait between alerts, so one busy camera doesn\'t flood your phone. The wait is counted per camera.',
      'Under Then, tick Raise an alarm on screen, the connections to send to, or both. Then save.',
      'Trigger it once to check it works. Failed deliveries appear under Recent events on the System page.',
    ],
    more: (
      <>
        <Option title="What each trigger does" steps={[
          'Motion: a camera reports motion. It needs a camera with ONVIF and a login.',
          'Camera analytics: a camera reports an analytics or audio event, such as a line crossing. Turn the analytic on in the camera first. To react to one kind only, type part of its name in Only events containing.',
          'Camera offline: a camera set to record stops sending video.',
          'Camera back online: that camera reconnects.',
          'Storage problem: recordings were deleted early to free space, or a storage location stopped working.',
          'Outside system: another system calls this rule\'s private web address.',
        ]} />
        <details>
          <summary>Letting another system trigger a rule</summary>
          <ol>
            <li>Add a rule with When set to Outside system. To get a picture, choose the camera that covers that door or area, and leave Include a snapshot ticked. Save it, and the rule list shows its private web address.</li>
            <li>In the other system, set it to send a POST request to that address. A message is optional:<pre>{hookExample}</pre></li>
            <li>The other system must be able to reach this server. Seelynx only accepts connections from its own machine unless it was started with the -listen option, and the Access list must allow the caller.</li>
          </ol>
          <p className="hint">Treat the address like a password, because anyone who has it can fire the rule. A cloud service can only reach it if you make this server reachable from the internet, so use HTTPS and take advice before doing that.</p>
        </details>
      </>
    ),
    notes: [
      'Messages can include {camera}, {event}, {topic} and {time}, which are filled in when the rule fires.',
      'Use high priority sparingly. If everything is high, people stop paying attention to alarms.',
      'Include a snapshot takes the picture from the recording. Seelynx searches the few seconds around the event and picks the frame showing the most movement, so alerts with a picture go out about 7 seconds after the event. The alarm itself appears straight away. If the camera wasn\'t recording then, Seelynx asks the camera for a picture instead. That needs ONVIF and a login, and it can be a few seconds late.',
      'A motion rule fires when motion starts. It fires again once motion has stopped for 5 seconds and the wait between alerts has passed, so later movement in the same stretch gets no picture of its own. It\'s all in the recording.',
      'Pictures reach email, Telegram, Discord, Pushover, ntfy and webhooks. Slack, Teams, PagerDuty, SMS and MQTT get the text only.',
      'If a picture can\'t be fetched or a service refuses it, the alert still goes out as text, and the reason appears under Recent events on the System page.',
    ],
  },
  connections: {
    title: 'Help: Connections',
    perm: 'settings',
    intro: 'A connection is a link to a service you already use, such as email, Slack or your phone. It does nothing until a rule uses it.',
    steps: [
      'Choose Add connection, then pick the service. Setup steps for that service appear in the form.',
      'Give it a name that says where messages go, such as Security team email.',
      'Fill in the details, then choose Save connection.',
      'Press Test and check that the message arrives. If it fails, the error says what the service answered.',
      'Tick the connection in a rule.',
    ],
    notes: [
      'Passwords and tokens are stored encrypted and never shown again. When you edit a connection, leave them as they are to keep them.',
      'A connection used by a rule can\'t be removed. Take it out of the rule first.',
      'To reach these services, this server needs internet access.',
      'For services that accept pictures, Test also sends a sample picture, so you can check both.',
    ],
  },
  ai: {
    title: 'Help: AI service',
    perm: 'settings',
    intro: 'Seelynx doesn\'t include an AI service. To build drivers, connect your own. You pay your provider for what you use, and a driver takes one short request.',
    steps: [
      'Choose your service. For a local model, choose OpenAI or a compatible server.',
      'Create an API key with your provider, using the steps below, and paste it in.',
      'Choose Save, then press Test.',
    ],
    more: (
      <>
        <Option title="Claude (Anthropic)" steps={[
          <>Sign in to the <Link to="https://console.anthropic.com">Claude Console</Link>, and add billing if your account has none.</>,
          'Under Settings, open API keys and create a key.',
          'Copy the key straight away and paste it here.',
        ]} note="A Claude.ai subscription doesn't cover API use, which is billed separately." />
        <Option title="OpenAI" steps={[
          <>Sign in to <Link to="https://platform.openai.com/api-keys">the OpenAI platform</Link> and create an API key.</>,
          'Choose OpenAI or a compatible server here, and enter the exact model name from your account.',
          'Leave Service address empty, and paste the key.',
        ]} />
        <Option title="A local model, such as Ollama" steps={[
          'Install Ollama on a computer that this server can reach, and download a model.',
          'Choose OpenAI or a compatible server here, and enter the model\'s name.',
          'Enter the address, such as http://localhost:11434/v1. Leave the API key empty if your server doesn\'t use one.',
        ]} note="Nothing leaves your network this way, but small models can write poor drivers, so check the result carefully." />
      </>
    ),
    notes: [
      'The key is stored encrypted and never shown again. It stays with one service: changing the service or address means entering a key again.',
      'Seelynx only contacts the service when you press Test or Build driver.',
    ],
  },
  drivers: {
    title: 'Help: Drivers',
    perm: 'settings',
    intro: 'A driver is a small file that describes a camera model. It gives the model\'s analytics names people understand, such as Line Cross, for rules and alerts. It can\'t run anything, and cameras work without one.',
    steps: [
      'Connect an AI service under Settings > AI service.',
      'Choose a camera with ONVIF and a login, then choose Read camera.',
      'Check what will be sent. It lists the make, model, firmware, video profiles and event names. Addresses, logins, serial numbers and video are never included.',
      'Choose Build driver, and wait for the AI service to answer.',
      'Check the names and descriptions, change any you want, and choose Save driver.',
      'Under Settings > Rules, choose Camera analytics. The Which analytic list now shows these names.',
    ],
    notes: [
      'One driver covers every camera of that make and model.',
      'Turn each analytic on in the camera\'s own settings. A driver names it, but doesn\'t enable it.',
      'Seelynx rejects any event the camera didn\'t report, so a driver can only describe what the camera says it can do. The descriptions are AI-written, so they may not match the camera exactly.',
      'Download saves a driver as a file, so you can keep or share it.',
    ],
  },
  groups: {
    title: 'Help: Camera groups',
    perm: 'settings',
    intro: 'Groups are folders for your cameras, such as buildings or floors. They keep the camera list on the Live page tidy.',
    steps: [
      'Type a group name and choose Add group.',
      'To nest it, choose another group under Inside. For example, put Floor 2 inside Main building.',
      'On the Cameras page, edit a camera and choose its group.',
    ],
    notes: ['Groups only organize the list. To limit who can see a camera, use a role on the Users page.'],
  },
  schedules: {
    title: 'Help: Recording schedules',
    perm: 'settings',
    intro: 'A schedule sets the hours when a camera records one way, and the rest of the time it can record another way or not at all. Cameras without one record the same way all the time. Rules can use schedules too, through Only during.',
    steps: [
      'Name the schedule, such as Outside business hours.',
      'Click days to turn them on or off, and set From and To.',
      'For different hours on other days, choose Add another period and set the next one.',
      'Choose Save schedule.',
      'On the Cameras page, edit a camera and choose the schedule.',
      'Under During the schedule, choose Keep all video or On motion only.',
      "Under Rest of the time, choose Don't record, Keep all video or On motion only.",
    ],
    notes: [
      'An end time earlier than the start runs overnight, so 18:00 to 08:00 covers the night.',
      'Times use the server\'s clock.',
      'A schedule used by a camera can\'t be removed. Choose another for that camera first.',
      'For example, to keep everything during business hours and only motion at night, make a Business hours schedule, then choose Keep all video during it and On motion only for the rest of the time.',
      'Changing a schedule or how a camera records never removes video already recorded for lack of motion. Only video recorded from then on follows the new setting.',
    ],
  },
  tours: {
    title: 'Help: Views and tours',
    perm: 'settings',
    intro: 'A view is a saved grid of cameras. A tour rotates through saved views on a timer, which suits a monitor on the wall.',
    steps: [
      'On the Live page, arrange a grid and choose Save view.',
      'Save at least one view, then come back here.',
      'Name the tour, set the seconds per view, and tick the views to include.',
      'Choose Add tour, then start it from the tour list on the Live page.',
    ],
    notes: ['A view used by a tour can\'t be removed. Remove the tour first.'],
  },
  access: {
    title: 'Help: Access list',
    perm: 'settings',
    intro: 'This limits which computers can connect to Seelynx at all, even to reach the sign-in page. It\'s a second layer on top of passwords.',
    steps: [
      'Add your own computer\'s address or network first, so you don\'t lock yourself out.',
      'Enter one address, such as 192.168.1.25, or a whole network, such as 192.168.1.0/24.',
      'Choose Add address. The list applies straight away.',
    ],
    notes: [
      'An empty list allows every computer that can reach the server.',
      'If you lock yourself out, open Seelynx on the server itself, which is always allowed.',
      'The list also applies to outside systems that call a rule\'s private web address.',
      'Seelynx only accepts connections from its own machine unless it was started with the -listen option.',
    ],
  },
  backup: {
    title: 'Help: Backup and restore',
    perm: 'settings',
    intro: 'Backups hold cameras, users and settings, but not video. One runs every day, and the latest 14 are kept in the data folder.',
    steps: [
      'Choose Back up now before a big change.',
      'Choose Download to keep a copy on another computer or drive. A backup on the same disk won\'t survive that disk failing.',
      'Copy data\\secret.key somewhere safe and separate. Without it, saved camera passwords and connection secrets can\'t be read.',
      'To go back to a backup, choose Restore, confirm, then restart Seelynx to finish. If it runs as a Windows service, run Restart-Service Seelynx as an administrator.',
    ],
    notes: ['A restore keeps your current configuration as a copy. Recordings aren\'t changed.'],
  },
  logs: {
    title: 'Help: Logs',
    perm: 'settings',
    intro: 'Logs record what the server and video engine did, which helps find the cause of a problem.',
    steps: [
      'Note the time the problem happened.',
      'Choose Download logs to get a zip file.',
      'Send it to whoever is helping you, along with that time.',
    ],
    notes: ['Camera passwords in stream addresses are removed, but the logs still include camera addresses and usernames, so share them only with people you trust.'],
  },

  'connection-webhook': {
    title: 'How to set up a webhook',
    intro: 'A webhook sends each alert to a web address as JSON. Use it for automation tools and your own systems.',
    steps: [
      'Get a web address from the service that should receive alerts. The steps for common services are below.',
      'Paste it into Web address.',
      'If the service needs a key, enter it in Extra header, such as Authorization: Bearer abc123.',
      'Save, then press Test.',
      thenRule,
    ],
    more: (
      <>
        <Option title="Zapier" steps={[
          'Create a Zap and choose Webhooks by Zapier as the trigger.',
          'Under Event, choose Catch Hook, then Continue.',
          'On the Test tab, copy the webhook address and paste it here.',
          'Save this connection and press Test, then choose Test trigger in Zapier.',
        ]} note="Zapier's webhooks need a paid plan." />
        <Option title="Make" steps={[
          'In the Scenario Builder, add Webhooks > Custom webhook.',
          'Next to Webhook, choose Add, enter a name, and choose Save.',
          'Copy the address and paste it here.',
          'If you added an API key, enter x-make-apikey: your-key in Extra header.',
        ]} />
        <Option title="n8n" steps={[
          'Add a Webhook node as the first step, and set its method to POST.',
          'Copy the Production URL and paste it here.',
          'Publish the workflow, because the Production URL only works after that.',
          'See each run on the workflow\'s Executions tab.',
        ]} />
        <Option title="Home Assistant" steps={[
          'Go to Settings > Automations & scenes, then Create automation > Create new automation.',
          'Under When, choose Add trigger and pick Webhook.',
          'Choose Copy URL to clipboard and paste it here.',
          'Add your actions and save the automation.',
        ]} note="Only accessible from the local network is on by default, which works when Seelynx is on the same network. The address needs no password, so keep it private." />
        <details>
          <summary>What Seelynx sends</summary>
          <pre>{webhookBody}</pre>
          <p className="hint">The snapshot field is only there when the rule includes one. It's a JPEG picture in base64.</p>
        </details>
      </>
    ),
  },
  'connection-slack': {
    title: 'How to set up Slack',
    steps: [
      <>Open <Link to="https://api.slack.com/apps?new_app=1">Create an app</Link> in Slack, pick a name and your workspace, and choose Create.</>,
      'In the app\'s settings, choose Incoming Webhooks and turn on Activate Incoming Webhooks.',
      'Choose Add New Webhook to Workspace, pick the channel for alerts, and choose Authorize.',
      'Copy the address under Webhook URLs for Your Workspace and paste it into Webhook address below.',
      'Save, then press Test.',
      thenRule,
    ],
    notes: ['Each address posts to one channel. To use another channel, add another webhook and connection.', 'The address is a secret. Slack revokes addresses it finds leaked.'],
  },
  'connection-teams': {
    title: 'How to set up Microsoft Teams',
    steps: [
      'In Teams, open Workflows.',
      'Search for the template Send webhook alerts to a channel.',
      'Choose the team and channel for alerts, then choose Save.',
      'On the workflow\'s details page, copy the webhook address and paste it into Workflow webhook address below.',
      'Save, then press Test.',
      thenRule,
    ],
    notes: ['Template names can differ between Teams versions. If you can\'t find it, search for webhook.', 'Seelynx sends no sign-in with its message, so the workflow must accept requests from Anyone.'],
  },
  'connection-discord': {
    title: 'How to set up Discord',
    steps: [
      'In Discord, open the server\'s settings. You need the Manage Webhooks permission.',
      'Go to Integrations > Webhooks and choose New Webhook.',
      'Name it, choose the channel for alerts, and choose Copy Webhook URL.',
      'Paste it into Webhook address below.',
      'Save, then press Test.',
      thenRule,
    ],
  },
  'connection-telegram': {
    title: 'How to set up Telegram',
    steps: [
      <>In Telegram, open <Link to="https://t.me/BotFather">@BotFather</Link> and send /newbot.</>,
      'Choose a name and a username ending in bot.',
      'Copy the token BotFather sends and paste it into Bot token below. Treat it like a password.',
      'Send your new bot a message. For a group, add the bot to the group and send a message there.',
      'In your browser, open https://api.telegram.org/bot<token>/getUpdates, with your token in place of <token>.',
      'Find "chat":{"id": in the text, and copy the number after it into Chat ID below, including any minus sign.',
      'Save, then press Test.',
      thenRule,
    ],
    notes: ['If getUpdates shows nothing, send the bot another message and reload. Telegram only keeps these for 24 hours.', 'In a group, the bot may not see normal messages. If nothing appears, send it a command such as /start.'],
  },
  'connection-gmail': {
    title: 'How to create an app password',
    steps: [
      'In your browser, sign in to the Google account that will send the alerts.',
      <>Open <Link to="https://myaccount.google.com/signinoptions/two-step-verification">2-Step Verification</Link>. If it's off, turn it on and follow the steps on screen. You need this before Google offers app passwords.</>,
      <>Open <Link to="https://myaccount.google.com/apppasswords">App passwords</Link>. Google may ask you to sign in again.</>,
      'Type a name you\'ll recognize, such as Seelynx, and choose Create.',
      'Copy the 16-character password Google shows. You can\'t view it again later, but you can always create a new one.',
      'Paste it into App password below, with or without the spaces. Fill in the other fields, save, then press Test.',
    ],
    notes: [
      'No App passwords page? Google doesn\'t offer it if 2-Step Verification uses only security keys, or if the account has Advanced Protection. On a work or school account, ask your admin.',
      'Changing the account\'s Google password cancels its app passwords. If email stops after that, create a new one and edit this connection.',
    ],
  },
  'connection-microsoft365': {
    title: 'How to set up Microsoft 365',
    steps: [
      <>Sign in to the <Link to="https://entra.microsoft.com">Microsoft Entra admin center</Link> as an admin of your organization.</>,
      'Go to Entra ID > App registrations and choose New registration. Name it Seelynx, choose Single tenant only under Supported account types, leave the redirect address empty, and choose Register.',
      'On the Overview page, copy the Application (client) ID and the Directory (tenant) ID into the fields below.',
      'Choose Certificates & secrets > Client secrets > New client secret. Add a description, pick an expiry of up to 24 months, and choose Add.',
      'Copy the secret\'s Value straight away into Client secret value below, because it\'s never shown again. The Secret ID is not what you need.',
      'Enter the mailbox to send from and the addresses to send to, then save.',
      'Give the app permission to send, using one of the two options below. Then press Test.',
    ],
    more: (
      <>
        <details>
          <summary>Option A: one mailbox only (recommended)</summary>
          <ol>
            <li>In the Entra admin center, go to Entra ID &gt; Enterprise applications and open Seelynx. Copy its Application ID and Object ID. Use these, not the IDs on the App registrations page, which are different.</li>
            <li>In PowerShell, as a member of the Exchange Organization Management role group, run these commands with your IDs and mailbox:<pre>{exchangeCommands}</pre></li>
            <li>The last command should show InScope as True.</li>
            <li>Wait before pressing Test. Microsoft says a change can take from 30 minutes to 2 hours to apply.</li>
          </ol>
          <p className="hint">Don't also add Mail.Send under API permissions. Microsoft adds the two grants together, so the app could then send as anyone.</p>
        </details>
        <Option title="Option B: whole organization (quicker, less safe)" steps={[
          'In App registrations, open Seelynx and go to API permissions.',
          'Choose Add a permission > Microsoft Graph > Application permissions, tick Mail.Send, and choose Add permissions.',
          'Choose Grant admin consent for your organization, then Yes.',
        ]} note="Anyone who gets this client secret could send email as any person in your organization." />
      </>
    ),
    notes: [
      'Client secrets expire. Put the date in your calendar, because email stops when it passes. Failed deliveries appear under Recent events on the System page.',
      'To replace a secret, create a new one, edit this connection and paste its Value.',
    ],
  },
  'connection-email': {
    title: 'How to set up another mail server',
    intro: 'Use this for your own mail server or a mail service such as your internet provider\'s. Their help pages list the settings, usually under SMTP.',
    steps: [
      'Enter the outgoing (SMTP) server in Mail server, such as smtp.example.com.',
      'Enter the port. 587 is the usual choice, and 465 is for servers that use TLS from the start. Leave it empty for 587.',
      'Enter the username and password, if your server needs a login.',
      'Enter a From address that your server allows you to send from.',
      'Enter the addresses that should receive alerts, separated by commas.',
      'Save, then press Test.',
      thenRule,
    ],
    notes: [
      'A password is never sent over an unencrypted connection, so a server without encryption only works without a login.',
      'Some services need an app password rather than your normal password. Check their help pages.',
      'For Gmail or Microsoft 365, use their own connection types instead.',
    ],
  },
  'connection-twilio': {
    title: 'How to set up SMS with Twilio',
    steps: [
      <>Create an account at <Link to="https://www.twilio.com/try-twilio">twilio.com</Link>, then upgrade it. See the note below on trial accounts.</>,
      <>Open the <Link to="https://1console.twilio.com/">Twilio Console</Link>. Copy the Account SID and Auth Token from the home page into the fields below.</>,
      'Buy a phone number that can send SMS, and enter it in From number, starting with + and the country code.',
      'Enter the phone to send alerts to in To number, in the same format.',
      'Save, then press Test.',
      thenRule,
    ],
    notes: [
      'Trial accounts won\'t work, because Twilio limits them to its own message templates. Seelynx sends its own text, so you need an upgraded account.',
      'To send to US phones, Twilio requires you to register your number first, using A2P 10DLC or toll-free verification. Allow time for this, and check the fees in Twilio\'s Console.',
      'Each connection sends to one number. To reach more people, add one connection for each.',
      'Twilio charges for each message, so set a sensible wait between alerts.',
    ],
  },
  'connection-pagerduty': {
    title: 'How to set up PagerDuty',
    steps: [
      'In PagerDuty, go to Services > Service Directory and open the service for your alerts.',
      'On the Integrations tab, choose Add another integration, pick Events API V2, and choose Add.',
      'Click the new integration in the list and copy its Integration Key.',
      'Paste it into Integration key below.',
      'Save, then press Test. This opens a real incident, so tell your on-call team first.',
      thenRule,
    ],
    notes: ['You need the Manager, Admin, Global Admin or Account Owner role to add an integration.', 'For a new service, choose New Service in the Service Directory and pick Events API V2 on the Integrations step.', 'High priority rules open critical incidents, medium opens warnings, and low opens info.'],
  },
  'connection-pushover': {
    title: 'How to set up Pushover',
    steps: [
      <>Sign in at <Link to="https://pushover.net/dashboard">pushover.net</Link>, and install the Pushover app on your phone.</>,
      'Copy Your User Key from the dashboard and paste it into User key below.',
      <>Open <Link to="https://pushover.net/apps/build">Create an Application</Link>, name it Seelynx, and create it.</>,
      'Copy its API token and paste it into App token below.',
      'Save, then press Test.',
      thenRule,
    ],
    notes: ['The phone app is free to try for 30 days, then a one-time purchase for each platform.', 'Each account can send 10,000 messages a month for free.', 'High priority rules skip the phone\'s quiet hours.'],
  },
  'connection-ntfy': {
    title: 'How to set up ntfy',
    steps: [
      'Choose a topic name that\'s hard to guess, using letters, numbers, dashes and underscores. Anyone who knows the name can read your alerts.',
      'Enter its address in Topic address below, such as https://ntfy.sh/seelynx-k3f9x2. For your own server, use that server\'s address.',
      <>Install the ntfy app on your phone, from the <Link to="https://docs.ntfy.sh/subscribe/phone/">ntfy website</Link>.</>,
      'In the app, add the same topic name.',
      'Save, then press Test.',
      thenRule,
    ],
    notes: ['Topics on ntfy.sh are public, and there\'s no sign-up, so the name works like a password. Don\'t put sensitive details in rule messages.', 'If you have an ntfy account or your own server, enter an access token to use protected topics.', 'The free ntfy.sh service limits how many messages you can send each day.'],
  },
  'connection-mqtt': {
    title: 'How to set up MQTT',
    intro: 'MQTT sends each alert as JSON to a broker, for Home Assistant, Node-RED and building systems.',
    steps: [
      'Make sure you have a broker running. In Home Assistant, the MQTT integration can set up the Mosquitto broker for you.',
      'Enter the broker in Broker address, such as 192.168.1.10:1883. Port 1883 is used if you leave it out.',
      'Choose a topic, such as seelynx/events.',
      'Enter a username and password if your broker needs them.',
      'Save, then press Test.',
      thenRule,
    ],
    more: (
      <Option title="See the messages in Home Assistant" steps={[
        'Go to Settings > Devices & services and open the Mosquitto broker.',
        'Choose Configure, then enter your topic under Listen to a topic.',
        'Press Test here and watch the message appear.',
      ]} />
    ),
    notes: ['This connection isn\'t encrypted, so use it only on a network you trust, and not across the internet.'],
  },
}

// The order topics appear on the Help page.
const order = ['start', 'live', 'playback', 'alarms', 'cameras', 'users', 'roles', 'system', 'storage', 'account', 'rules', 'connections', 'ai', 'drivers', 'groups', 'schedules', 'tours', 'access', 'backup', 'logs']

export function Guide({ topic, open, className }: { topic: string; open?: boolean; className?: string }) {
  const t = topics[topic]
  if (!t) return null
  return (
    <details className={`guide ${className ?? ''}`} open={open}>
      <summary>{t.title}</summary>
      {t.intro && <p>{t.intro}</p>}
      {t.steps && <ol>{t.steps.map((s, i) => <li key={i}>{s}</li>)}</ol>}
      {t.more}
      {t.notes?.map((n, i) => <p className="hint" key={i}>{n}</p>)}
    </details>
  )
}

export function HelpPage({ permissions }: { permissions: Permission[] }) {
  const shown = order.filter((id) => !topics[id].perm || permissions.includes(topics[id].perm))
  const services = Object.keys(topics).filter((id) => id.startsWith('connection-'))
  return (
    <div className="page">
      <div className="toolbar"><h2>Help</h2></div>
      <p className="muted">Every page has its own help at the top. It's all collected here too.</p>
      {shown.map((id) => <Guide key={id} topic={id} open={id === 'start'} />)}
      {permissions.includes('settings') && <>
        <h3>Connecting a service</h3>
        {services.map((id) => <Guide key={id} topic={id} />)}
      </>}
    </div>
  )
}
